import { useEffect, useRef, useState } from 'react'
import type { Stop } from '../api'
import { aspectLabel, platformOf, type Aspect } from '../format'
import { subscribe, useLiveStatus } from '../live'
import { Flap } from './Flap'

/** A small signal head showing an aspect. Double yellow has two lamps. */
export function Signal({ aspect, label }: { aspect: Aspect; label?: string }) {
  const lamps = aspect === 'double' ? 2 : 1
  return (
    <span className={`signal signal-${aspect}`} role="img" aria-label={label ?? aspectLabel[aspect]} title={label ?? aspectLabel[aspect]}>
      {Array.from({ length: lamps }, (_, i) => (
        <span key={i} className="lamp" />
      ))}
    </span>
  )
}

/**
 * A platform number on a plate. Confirmed is a solid plate, booked or not yet
 * announced is an outline, changed is amber. The number flips when it changes.
 */
export function PlatformSign({ stop, lead = false, introduce = false, delay = 0 }: { stop: Stop; lead?: boolean; introduce?: boolean; delay?: number }) {
  const p = platformOf(stop)
  if (!p) return <span className="platform platform-empty" role="img" aria-label="Platform not known" />
  const changed = stop.platform?.changed
  const state = changed ? ' platform-changed' : stop.platform?.confirmed ? '' : ' platform-unconfirmed'
  const plate = (
    <span
      className={`platform${state}${lead ? ' platform-lead' : ''}`}
      title={
        changed
          ? `Changed from platform ${stop.platform?.planned}`
          : stop.platform?.confirmed
            ? 'Confirmed'
            : stop.platform?.suppressed
              ? 'Booked platform. The station hasn’t announced it yet, so it may change.'
              : 'Booked platform'
      }
    >
      <Flap
        value={p}
        tiles={lead}
        introduce={introduce}
        delay={delay}
        label={`Platform ${p}${changed ? `, changed from ${stop.platform?.planned}` : stop.platform?.confirmed ? '' : ', booked'}`}
      />
    </span>
  )
  // A change is said in words too, never by the amber alone.
  if (!changed || !stop.platform?.planned) return plate
  return (
    <span className="plat-changed">
      {plate}
      <span className="plat-was" aria-hidden="true">
        was {stop.platform.planned}
      </span>
    </span>
  )
}

/** Says once what the two plate styles mean. */
export function PlatformKey() {
  return (
    <span className="plat-key">
      <span className="platform platform-key" aria-hidden="true" />
      confirmed platform
      <span className="platform platform-unconfirmed platform-key" aria-hidden="true" />
      booked, may still change
    </span>
  )
}

export interface LiveOptions {
  /** Refetch when the server pushes a change under this topic. */
  topic?: string
  /** The least time between pushed refetches, for heavy loads like the map. */
  minGapMs?: number
}

/**
 * Loads data, keeps the last good result, and refreshes it. With a topic, it
 * refetches when the server reports a change and polls only as a slow
 * fallback while pushes are arriving; without one, or with the socket down,
 * it polls every intervalMs.
 */
export function usePolling<T>(
  load: (signal: AbortSignal) => Promise<T>,
  deps: unknown[],
  intervalMs: number,
  { topic, minGapMs = 2000 }: LiveOptions = {},
) {
  const [data, setData] = useState<T>()
  const [error, setError] = useState<Error>()
  const [loading, setLoading] = useState(true)
  const loadRef = useRef(load)
  loadRef.current = load
  const runRef = useRef<() => void>(() => {})

  useEffect(() => {
    let ctrl = new AbortController()
    let stopped = false
    setLoading(true)
    setData(undefined)
    setError(undefined)
    const run = async () => {
      ctrl.abort()
      ctrl = new AbortController()
      try {
        const d = await loadRef.current(ctrl.signal)
        if (!stopped) {
          setData(d)
          setError(undefined)
        }
      } catch (e) {
        if (!stopped && (e as Error).name !== 'AbortError') setError(e as Error)
      } finally {
        if (!stopped) setLoading(false)
      }
    }
    runRef.current = () => {
      if (document.visibilityState === 'visible') run()
    }
    run()
    // Coming back to the tab shouldn't mean waiting out the interval on old data.
    const onVisible = () => runRef.current()
    document.addEventListener('visibilitychange', onVisible)
    return () => {
      stopped = true
      ctrl.abort()
      document.removeEventListener('visibilitychange', onVisible)
    }
  }, deps) // eslint-disable-line react-hooks/exhaustive-deps

  const { connected, fallback } = useLiveStatus()
  const live = !!topic && connected
  const every = live ? Math.max(intervalMs, fallback * 1000) : intervalMs
  useEffect(() => {
    const id = window.setInterval(() => runRef.current(), every)
    return () => window.clearInterval(id)
  }, [every])

  useEffect(() => {
    if (!topic) return
    // Changes arrive in bursts: wait for the burst to settle, and never
    // refetch more often than minGapMs.
    let timer: number | undefined
    let last = 0
    const off = subscribe(topic, () => {
      window.clearTimeout(timer)
      const wait = Math.max(300, last + minGapMs - Date.now())
      timer = window.setTimeout(() => {
        last = Date.now()
        runRef.current()
      }, wait)
    })
    return () => {
      off()
      window.clearTimeout(timer)
    }
  }, [topic, minGapMs])

  return { data, error, loading, live }
}

export function ErrorNote({ error }: { error: Error }) {
  return <p className="note note-error">{error.message}</p>
}

/**
 * Renders a station message's simple HTML safely: only text, paragraphs
 * and http(s) links survive; everything else is reduced to its text.
 */
export function MessageText({ html, text }: { html: string; text: string }) {
  if (!html) return <>{text}</>
  const doc = new DOMParser().parseFromString(html, 'text/html')
  const walk = (node: Node, key: string): React.ReactNode => {
    if (node.nodeType === Node.TEXT_NODE) return node.textContent
    if (node.nodeType !== Node.ELEMENT_NODE) return null
    const el = node as Element
    const kids = Array.from(el.childNodes).map((c, i) => walk(c, `${key}.${i}`))
    const tag = el.tagName.toLowerCase()
    if (tag === 'a') {
      const href = el.getAttribute('href') ?? ''
      if (/^https?:\/\//i.test(href)) {
        return (
          <a key={key} href={href} target="_blank" rel="noopener noreferrer">
            {kids}
          </a>
        )
      }
    }
    if (tag === 'p') return <span key={key} className="message-para">{kids}</span>
    return <span key={key}>{kids}</span>
  }
  return <>{Array.from(doc.body.childNodes).map((n, i) => walk(n, String(i)))}</>
}

import { useEffect, useRef, useState } from 'react'
import type { Stop } from '../api'
import { aspectLabel, platformOf, type Aspect } from '../format'

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

/** A platform number in the style of a platform sign. */
export function PlatformSign({ stop }: { stop: Stop }) {
  const p = platformOf(stop)
  if (!p) return <span className="platform platform-empty" aria-label="Platform not known" />
  const changed = stop.platform?.changed
  return (
    <span
      className={`platform${changed ? ' platform-changed' : ''}${stop.platform?.confirmed ? '' : ' platform-unconfirmed'}`}
      aria-label={`Platform ${p}${changed ? `, changed from ${stop.platform?.planned}` : ''}`}
      title={changed ? `Changed from platform ${stop.platform?.planned}` : stop.platform?.confirmed ? 'Confirmed' : 'Planned'}
    >
      {p}
    </span>
  )
}

/** Re-runs a loader every interval, keeping the last good result. */
export function usePolling<T>(load: (signal: AbortSignal) => Promise<T>, deps: unknown[], intervalMs: number) {
  const [data, setData] = useState<T>()
  const [error, setError] = useState<Error>()
  const [loading, setLoading] = useState(true)
  const loadRef = useRef(load)
  loadRef.current = load
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
    run()
    const id = window.setInterval(() => {
      if (document.visibilityState === 'visible') run()
    }, intervalMs)
    return () => {
      stopped = true
      ctrl.abort()
      window.clearInterval(id)
    }
  }, deps) // eslint-disable-line react-hooks/exhaustive-deps
  return { data, error, loading }
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

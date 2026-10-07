import { useSyncExternalStore } from 'react'

/**
 * One WebSocket per tab to /v1/live. Pages subscribe to topics
 * ("station:WAT", "train:W12345|2026-10-06", "map") and are called when the
 * server says something under that topic changed; they then refetch through
 * the REST API. The socket opens with the first subscriber, reconnects with
 * backoff, and after a reconnect calls every subscriber once, since changes
 * may have been missed while it was down.
 */

type Listener = () => void

const listeners = new Map<string, Set<Listener>>()
let socket: WebSocket | undefined
let retries = 0
let retryTimer: number | undefined
let idleTimer: number | undefined
let everOpened = false

interface Status {
  connected: boolean
  /** Seconds between polls the server still wants while connected. */
  fallback: number
}
let status: Status = { connected: false, fallback: 120 }
const statusListeners = new Set<() => void>()

function setStatus(next: Status) {
  if (next.connected === status.connected && next.fallback === status.fallback) return
  status = next
  statusListeners.forEach(f => f())
}

function send(type: 'subscribe' | 'unsubscribe', topics: string[]) {
  if (socket?.readyState === WebSocket.OPEN && topics.length) socket.send(JSON.stringify({ type, topics }))
}

function connect() {
  window.clearTimeout(retryTimer)
  if (socket || listeners.size === 0) return
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
  const ws = new WebSocket(`${proto}//${location.host}/v1/live`)
  socket = ws
  ws.onopen = () => send('subscribe', [...listeners.keys()])
  ws.onmessage = e => {
    let m: { type?: string; topics?: string[]; fallback?: number }
    try {
      m = JSON.parse(e.data)
    } catch {
      return
    }
    if (m.type === 'hello') {
      retries = 0
      setStatus({ connected: true, fallback: m.fallback ?? status.fallback })
      // Anything could have changed while we were disconnected.
      if (everOpened) listeners.forEach(set => set.forEach(f => f()))
      everOpened = true
    } else if (m.type === 'changed') {
      for (const t of m.topics ?? []) listeners.get(t)?.forEach(f => f())
    }
  }
  ws.onclose = () => {
    if (socket !== ws) return
    socket = undefined
    setStatus({ ...status, connected: false })
    if (listeners.size === 0) return
    // 1 s, 2 s, 4 s … up to 30 s, with jitter so a restart isn't a stampede.
    const delay = Math.min(30_000, 1000 * 2 ** retries) * (0.5 + Math.random() / 2)
    retries++
    retryTimer = window.setTimeout(connect, delay)
  }
}

// Reconnect at once when the network or the tab comes back.
window.addEventListener('online', () => connect())
document.addEventListener('visibilitychange', () => {
  if (document.visibilityState === 'visible' && !socket) connect()
})

/** Calls fn whenever the server reports a change under topic. Returns an unsubscribe. */
export function subscribe(topic: string, fn: Listener): () => void {
  window.clearTimeout(idleTimer)
  let set = listeners.get(topic)
  if (!set) {
    set = new Set()
    listeners.set(topic, set)
    send('subscribe', [topic])
  }
  set.add(fn)
  connect()
  return () => {
    set.delete(fn)
    if (set.size > 0 || listeners.get(topic) !== set) return
    listeners.delete(topic)
    send('unsubscribe', [topic])
    // A page with nothing live (Delay Repay) doesn't need the socket; close
    // it after a moment, in case the next page subscribes straight away.
    if (listeners.size === 0) {
      idleTimer = window.setTimeout(() => {
        if (listeners.size === 0 && socket) {
          const ws = socket
          socket = undefined
          ws.close(1000, 'idle')
          setStatus({ ...status, connected: false })
        }
      }, 15_000)
    }
  }
}

/** Whether pushes are arriving, and how often to poll anyway. */
export function useLiveStatus(): Status {
  return useSyncExternalStore(
    f => {
      statusListeners.add(f)
      return () => statusListeners.delete(f)
    },
    () => status,
  )
}

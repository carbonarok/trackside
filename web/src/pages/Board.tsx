import { useEffect, useState } from 'react'
import { Link, useParams, useSearchParams } from 'react-router-dom'
import { api, type BoardService, type Location } from '../api'
import { StationPicker } from '../components/Search'
import { ErrorNote, MessageText, PlatformSign, Signal, usePolling } from '../components/ui'
import { bookedTime, eventStatus } from '../format'

export function Board() {
  const { code = '' } = useParams()
  const [params, setParams] = useSearchParams()
  const arrivals = params.get('view') === 'arrivals'
  const via = params.get('via') ?? undefined
  const at = params.get('at') ?? undefined
  const [viaLocation, setViaLocation] = useState<Location>()

  const { data, error, loading } = usePolling(
    signal =>
      api.board(code, arrivals ? 'arrivals' : 'departures', { at, window: '180', [arrivals ? 'from' : 'to']: via }, signal),
    [code, arrivals, via, at],
    30_000,
  )

  useEffect(() => {
    if (!via) setViaLocation(undefined)
  }, [via])

  useEffect(() => {
    if (data) document.title = `${data.location.name} ${arrivals ? 'arrivals' : 'departures'} · trackside`
  }, [data, arrivals])

  const set = (k: string, v?: string) => {
    const next = new URLSearchParams(params)
    if (v) next.set(k, v)
    else next.delete(k)
    setParams(next, { replace: true })
  }

  return (
    <div className="page board-page">
      <header className="page-head">
        <h1>
          {data?.location.name ?? (loading ? ' ' : code)}
          {data?.location.crs && <span className="code">{data.location.crs}</span>}
        </h1>
        <div className="board-controls">
          <div className="tabs" role="tablist" aria-label="Board">
            <button role="tab" aria-selected={!arrivals} onClick={() => set('view')}>
              Departures
            </button>
            <button role="tab" aria-selected={arrivals} onClick={() => set('view', 'arrivals')}>
              Arrivals
            </button>
          </div>
          <StationPicker
            label={arrivals ? 'Coming from' : 'Calling at'}
            value={viaLocation}
            onChange={l => {
              setViaLocation(l)
              set('via', l?.crs)
            }}
          />
          <div className="field">
            <label htmlFor="at">From</label>
            <input
              id="at"
              type="datetime-local"
              value={at ?? ''}
              onChange={e => set('at', e.target.value || undefined)}
            />
          </div>
          {at && (
            <button className="link-button" onClick={() => set('at')}>
              Back to now
            </button>
          )}
        </div>
      </header>

      {data?.messages.map(m => (
        <aside key={`${m.id}-${m.text.slice(0, 20)}`} className={`message severity-${m.severity}`}>
          <MessageText html={m.html} text={m.text} />
        </aside>
      ))}

      {error && !data && <ErrorNote error={error} />}
      {data && data.services.length === 0 && (
        <p className="note">
          No {arrivals ? 'arrivals' : 'departures'} in the next three hours{via ? ' matching that station' : ''}.
        </p>
      )}
      {data && data.services.length > 0 && (
        <ol className="board" aria-label={arrivals ? 'Arrivals' : 'Departures'}>
          {data.services.map(s => (
            <BoardRow key={`${s.uid}-${s.runDate}`} service={s} arrivals={arrivals} />
          ))}
        </ol>
      )}
      {data && <p className="refreshed">Updates every 30 seconds.</p>}
    </div>
  )
}

function BoardRow({ service: s, arrivals }: { service: BoardService; arrivals: boolean }) {
  const t = arrivals ? s.stop.arrival : s.stop.departure
  const status = eventStatus(s.stop, t, arrivals)
  const place = arrivals ? s.origin[0]?.name : s.destination[0]?.name
  return (
    <li className={`board-row${status.aspect === 'red' && status.text === 'Cancelled' ? ' is-cancelled' : ''}`}>
      <Link to={`/train/${s.uid}/${s.runDate}`} className="board-link">
        <span className="board-time">{bookedTime(t)}</span>
        <span className="board-place">
          <span className="board-dest">{place}</span>
          <span className="board-sub">
            {s.operator?.name ?? 'Operator not known'}
            {s.headcode && <span className="headcode">{s.headcode}</span>}
          </span>
        </span>
        <PlatformSign stop={s.stop} />
        <span className="board-status">
          <Signal aspect={status.aspect} />
          <span>
            <span className="status-text">{status.text}</span>
            {status.detail && <span className="status-detail">{status.detail}</span>}
          </span>
        </span>
      </Link>
    </li>
  )
}

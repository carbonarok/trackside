import { useEffect, useRef, useState } from 'react'
import { Link, useParams, useSearchParams } from 'react-router-dom'
import { api, type BoardService, type Location } from '../api'
import { Flap } from '../components/Flap'
import { PlatformFilter, platformList } from '../components/PlatformFilter'
import { StationPicker } from '../components/Search'
import { ErrorNote, MessageText, PlatformKey, PlatformSign, Signal, usePolling } from '../components/ui'
import { bookedTime, eventStatus, hhmm, platformOf } from '../format'

const minutesBetween = (a?: string, b?: string) =>
  a && b ? Math.round((new Date(b).getTime() - new Date(a).getTime()) / 60_000) : undefined

const duration = (mins: number) => {
  const h = Math.floor(mins / 60)
  const m = mins % 60
  return h ? `${h} h ${m} min` : `${m} min`
}

/** Minutes from origin to destination for the part of the journey this board shows. */
function journeyOf(s: BoardService, arrivals: boolean) {
  if (arrivals) {
    const at = s.stop.arrival?.public ?? s.stop.arrival?.working
    return { mins: minutesBetween(s.origin[0]?.time, at), end: s.origin[0]?.time }
  }
  const at = s.stop.departure?.public ?? s.stop.departure?.working
  const dest = s.destination[s.destination.length - 1]?.time
  return { mins: minutesBetween(at, dest), end: dest }
}

export function Board() {
  const { code = '' } = useParams()
  const [params, setParams] = useSearchParams()
  const arrivals = params.get('view') === 'arrivals'
  const via = params.get('via') ?? undefined
  const at = params.get('at') ?? undefined
  const plats = (params.get('plat') ?? '').split(',').filter(Boolean)
  const [viaLocation, setViaLocation] = useState<Location>()

  const { data, error, loading, live } = usePolling(
    signal =>
      api.board(code, arrivals ? 'arrivals' : 'departures', { at, window: '180', [arrivals ? 'from' : 'to']: via }, signal),
    [code, arrivals, via, at],
    30_000,
    { topic: `station:${code.toUpperCase()}` },
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

  const all = data?.services ?? []
  // Platforms at this station on the board now, in the order the station numbers them.
  const platforms = [...new Set([...all.map(s => platformOf(s.stop)), ...plats].filter((p): p is string => !!p))].sort((a, b) =>
    a.localeCompare(b, 'en-GB', { numeric: true }),
  )
  // A train moved off a chosen platform still shows there, marked "was", so
  // anyone waiting on the old platform learns it moved.
  const onPlats = (s: BoardService) =>
    plats.includes(platformOf(s.stop) ?? '') || plats.includes(s.stop.platform?.planned ?? '')
  const services = plats.length ? all.filter(onPlats) : all
  const longest = Math.max(60, ...services.map(s => journeyOf(s, arrivals).mins ?? 0))
  // The next train leads: the first that hasn't gone yet.
  const lead = services.findIndex(s => {
    const t = arrivals ? s.stop.arrival : s.stop.departure
    return !t?.actual && !s.stop.cancelled && !t?.cancelled
  })
  const view = arrivals ? 'Arrivals' : 'Departures'

  return (
    <div className="page board-page">
      <header className="table-head">
        <h1>
          <span className="table-head-name">{data?.location.name ?? (loading ? ' ' : code.toUpperCase())}</span>
          {data?.location.crs && <span className="crs">{data.location.crs}</span>}
        </h1>
        {data && (
          <p className="table-head-span">
            {hhmm(data.from)} to {hhmm(data.to)}
          </p>
        )}
      </header>

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
          placeholder="Any station"
          value={viaLocation}
          onChange={l => {
            setViaLocation(l)
            set('via', l?.crs)
          }}
        />
        <WhenField value={at} onChange={v => set('at', v)} />
        <PlatformFilter platforms={platforms} selected={plats} onChange={v => set('plat', v.length ? v.join(',') : undefined)} />
        {at && (
          <button className="link-button" onClick={() => set('at')}>
            Back to now
          </button>
        )}
      </div>

      {data && data.messages.length > 0 && (
        <ul className="notices" aria-label="Station notices">
          {data.messages.map(m => (
            <li key={`${m.id}-${m.text.slice(0, 20)}`} className={`notice severity-${m.severity}`}>
              <span className="notice-label">{m.severity >= 3 ? 'Severe' : m.severity === 0 ? 'Info' : 'Notice'}</span>
              <span className="notice-text">
                <MessageText html={m.html} text={m.text} />
              </span>
            </li>
          ))}
        </ul>
      )}

      {error && !data && <ErrorNote error={error} />}
      {!data && !error && <p className="note">Loading the board…</p>}
      {data && services.length === 0 && (
        <p className="note">
          No {view.toLowerCase()}{' '}
          {plats.length ? `${arrivals ? 'into' : 'from'} platform${plats.length > 1 ? 's' : ''} ${platformList(plats)} ` : ''}in the next
          three hours{via ? ' matching that station' : ''}.
          {plats.length > 0 && (
            <>
              {' '}
              <button className="link-button" onClick={() => set('plat')}>
                Show all platforms
              </button>
            </>
          )}
        </p>
      )}
      {services.length > 0 && (
        <div className={`board${arrivals ? ' board-arrivals' : ''}`}>
          <div className="board-heads" aria-hidden="true">
            <span>{arrivals ? 'Arr' : 'Dep'}</span>
            <span>{arrivals ? 'From' : 'Destination'}</span>
            <span>{arrivals ? 'Left origin' : 'Arrives'}</span>
            <span>Plat</span>
            <span>Running</span>
          </div>
          <ol aria-label={view}>
            {services.map((s, i) => (
              <BoardRow key={`${s.uid}-${s.runDate}`} service={s} arrivals={arrivals} lead={i === lead} index={i} longest={longest} />
            ))}
          </ol>
        </div>
      )}
      {data && (
        <p className="refreshed">
          <PlatformKey />
          <span>
            {live ? 'Live: updates as trains report.' : 'Updates every 30 seconds.'} Times and platforms flip when they
            change.
          </span>
        </p>
      )}
    </div>
  )
}

const whenFmt = new Intl.DateTimeFormat('en-GB', { weekday: 'short', day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' })

/**
 * "From": now, or a chosen moment. A plain button opens the browser's own
 * date and time picker, so the empty field never shows a dd/mm/yyyy mask.
 */
function WhenField({ value, onChange }: { value?: string; onChange: (v?: string) => void }) {
  const input = useRef<HTMLInputElement>(null)
  const [typing, setTyping] = useState(false)
  const open = () => {
    const el = input.current
    if (!el) return
    if (typeof el.showPicker === 'function') {
      try {
        el.showPicker()
        return
      } catch {
        // not allowed here; fall back to the field itself
      }
    }
    setTyping(true)
    window.setTimeout(() => el.focus())
  }
  return (
    <div className="field when">
      <label htmlFor="at">From</label>
      <button type="button" className={`when-button${typing ? ' sr-only' : ''}`} onClick={open} aria-describedby="at-now">
        {value ? whenFmt.format(new Date(value)) : 'Now'}
      </button>
      <span id="at-now" className="sr-only">
        Choose a date and time to see the board from then
      </span>
      <input
        ref={input}
        id="at"
        type="datetime-local"
        className={typing ? undefined : 'when-input'}
        tabIndex={typing ? 0 : -1}
        value={value ?? ''}
        onChange={e => onChange(e.target.value || undefined)}
        onBlur={() => setTyping(false)}
      />
    </div>
  )
}

function BoardRow({
  service: s,
  arrivals,
  lead,
  index,
  longest,
}: {
  service: BoardService
  arrivals: boolean
  lead: boolean
  index: number
  longest: number
}) {
  const t = arrivals ? s.stop.arrival : s.stop.departure
  const status = eventStatus(s.stop, t, arrivals)
  const cancelled = status.text === 'Cancelled'
  const places = (arrivals ? s.origin : s.destination).map(e => e.name).join(' and ')
  const journey = journeyOf(s, arrivals)
  const expected = status.text.startsWith('Expected ') ? status.text.slice(9) : undefined
  const delay = Math.min(index, 14) * 40
  const reason = cancelled ? s.cancelReason || s.cancelReasonCodeDescription : undefined
  const span = journey.mins && journey.mins > 0 ? Math.max(0.08, Math.min(1, journey.mins / longest)) : 0

  return (
    <li className={`row${lead ? ' row-lead' : ''}${cancelled ? ' is-cancelled' : ''} aspect-${status.aspect}`}>
      <Link to={`/train/${s.uid}/${s.runDate}`} className="row-link">
        <span className="col-time">
          <Flap value={bookedTime(t)} tiles={lead} introduce delay={delay} />
        </span>
        <span className="col-place">
          <span className="dest">{places}</span>
          <span className="sub">
            <span>{s.operator?.name ?? 'Operator not known'}</span>
            {s.headcode && <span className="headcode">{s.headcode}</span>}
            {journey.end && (
              <span className="sub-arr">
                {arrivals ? 'left' : 'arr'} {hhmm(journey.end)}
              </span>
            )}
          </span>
          {reason && <span className="reason">{reason}</span>}
        </span>
        <span className="col-journey">
          {journey.end && span > 0 && (
            <span className="journey" style={{ '--span': span } as React.CSSProperties}>
              <span className="journey-rule" aria-hidden="true" />
              <span className="journey-time" title={`Journey ${duration(journey.mins!)}`}>
                <span className="sr-only">{arrivals ? 'Left its origin at' : 'Arrives at its destination at'} </span>
                {hhmm(journey.end)}
                <span className="sr-only">, a journey of {duration(journey.mins!)}</span>
              </span>
            </span>
          )}
        </span>
        <span className="col-plat">
          <PlatformSign stop={s.stop} introduce delay={delay + 120} />
        </span>
        <span className="col-run">
          <Signal aspect={status.aspect} />
          <span className="run-text">
            {expected ? (
              <span className="run-main run-late">
                Exp <Flap value={expected} label={expected} />
              </span>
            ) : (
              <span className="run-main">{status.text}</span>
            )}
            {status.detail && <span className="run-detail">{status.detail}</span>}
          </span>
        </span>
      </Link>
    </li>
  )
}

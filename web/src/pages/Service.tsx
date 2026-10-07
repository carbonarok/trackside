import { useEffect, useMemo, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { api, type Association, type ServiceDetail, type Stop, type Times } from '../api'
import { Flap } from '../components/Flap'
import { ErrorNote, PlatformSign, Signal, usePolling } from '../components/ui'
import { aspectFor, bookedTime, hhmm, lateness, longDate } from '../format'

const statusText: Record<ServiceDetail['status'], string> = {
  scheduled: 'Not yet running',
  activated: 'Ready to depart',
  running: 'Running',
  terminated: 'Arrived',
  cancelled: 'Cancelled',
  partially_cancelled: 'Part cancelled',
}

const associationText: Record<Association['type'], string> = {
  divides: 'Divides here. The rear portion runs',
  divided_from: 'Formed by dividing from',
  joined_by: 'Joined here by',
  joins: 'Joins',
  forms: 'Then runs as',
  formed_from: 'Formed from',
  linked: 'Linked with',
  associated: 'Associated with',
}

export function Service() {
  const { uid = '', date = '' } = useParams()
  const [showPasses, setShowPasses] = useState(false)
  const { data: svc, error, live } = usePolling(signal => api.service(uid, date, signal), [uid, date], 30_000, {
    topic: `train:${uid}|${date}`,
  })

  useEffect(() => {
    if (svc) document.title = `${svc.headcode ?? svc.uid} ${svc.origin[0]?.name} to ${svc.destination[0]?.name} · trackside`
  }, [svc])

  // The train is between the last stop it has reported at and the next.
  const progress = useMemo(() => {
    if (!svc) return -1
    let last = -1
    svc.stops.forEach((s, i) => {
      if (s.arrival?.actual || s.departure?.actual || s.pass?.actual) last = i
    })
    return last
  }, [svc])

  // How the train is running, from the last place it reported.
  const running = (() => {
    if (!svc) return { aspect: 'unlit' as const, late: undefined }
    if (svc.status === 'cancelled') return { aspect: 'red' as const, late: undefined }
    const s = svc.stops[progress]
    const t = s && (s.departure?.actual ? s.departure : s.arrival?.actual ? s.arrival : s.pass)
    if (!t?.actual) return { aspect: 'unlit' as const, late: undefined }
    return { aspect: aspectFor(t.delayMinutes, { live: true }), late: lateness(t.delayMinutes) }
  })()

  if (error && !svc)
    return (
      <div className="page">
        <ErrorNote error={error} />
      </div>
    )
  if (!svc)
    return (
      <div className="page">
        <p className="note">Loading train…</p>
      </div>
    )

  const origin = svc.origin[0]
  const dest = svc.destination[0]
  const stops = svc.stops
    .map((s, i) => ({ s, i }))
    .filter(({ s }) => showPasses || s.kind !== 'pass' || svc.stops.indexOf(s) === progress)
  const passes = svc.stops.filter(s => s.kind === 'pass').length
  const reason = svc.cancelReason || svc.cancelReasonCodeDescription || svc.lateReason

  return (
    <div className="page service-page">
      <header className="service-head">
        <div className="table-head">
          <h1>
            {origin?.name} to {dest?.name}
          </h1>
          <p className="service-meta">
            {svc.headcode && <span className="headcode">{svc.headcode}</span>}
            <span>{svc.operator?.name ?? 'Operator not known'}</span>
            <span>{longDate(svc.runDate)}</span>
          </p>
          <p className={`service-status status-${svc.status}`}>
            <Signal aspect={running.aspect} />
            <span>
              {statusText[svc.status]}
              {svc.plannedCancel && ', cancelled in the timetable'}
              {running.late && `, ${running.late}`}
            </span>
          </p>
        </div>
        {reason && <p className="reason">{reason}</p>}
        <div className="actions">
          <Link className="button" to={`/map?train=${svc.uid}|${svc.runDate}`}>
            Show on the map
          </Link>
          <Link
            className="button button-quiet"
            to={`/delay-repay?from=${origin?.crs ?? ''}&to=${dest?.crs ?? ''}&date=${svc.runDate}&departure=${hhmm(origin?.time)}`}
          >
            Check Delay Repay
          </Link>
        </div>
      </header>

      {passes > 0 && (
        <label className="toggle">
          <input type="checkbox" checked={showPasses} onChange={e => setShowPasses(e.target.checked)} />
          Show {passes} places it passes without stopping
        </label>
      )}

      <div className="route-heads" aria-hidden="true">
        <span>Times</span>
        <span />
        <span>Calling at</span>
        <span>Plat</span>
      </div>
      <ol className="route" aria-label="Route">
        {stops.map(({ s, i }) => (
          <RouteStop
            key={`${s.tiploc}-${i}`}
            stop={s}
            passed={i <= progress}
            here={i === progress || s.atPlatform}
            first={i === 0}
            last={i === svc.stops.length - 1}
            associations={(svc.associations ?? []).filter(a => a.location.tiploc === s.tiploc)}
          />
        ))}
      </ol>
      <p className="refreshed">
        {live ? 'Live: updates as the train reports.' : 'Updates every 30 seconds.'} Train ID {svc.uid}
        {svc.source === 'vstp' ? ', added at short notice' : ''}.
      </p>
    </div>
  )
}

function TimeCell({ t, label }: { t?: Times; label: string }) {
  if (!t || (!t.public && !t.working)) return <span className="rt-empty" />
  const booked = bookedTime(t)
  const live = t.actual ?? t.estimated
  const changed = live && hhmm(live) !== booked
  return (
    <span className={`rt${t.cancelled ? ' rt-cancelled' : ''}`}>
      <span className="rt-label">{label}</span>
      {/* When it ran to time, one time says it all. */}
      <span className={`rt-booked${changed ? ' rt-struck' : ''}${t.actual && !changed ? ' rt-actual' : ''}`}>{booked}</span>
      {t.cancelled ? (
        <span className="rt-live">Cancelled</span>
      ) : t.delayed && !t.actual ? (
        <span className="rt-live">Delayed</span>
      ) : (
        changed && (
          <span
            className={`rt-live${t.actual && !(t.delayMinutes && t.delayMinutes > 0) ? ' rt-actual' : ''}${t.delayMinutes !== undefined && t.delayMinutes < 0 ? ' rt-early' : ''}`}
            title={t.actual ? 'Actual' : 'Expected'}
          >
            {t.actual ? '' : 'exp '}
            <Flap value={hhmm(live)} />
          </span>
        )
      )}
    </span>
  )
}

function RouteStop({
  stop,
  passed,
  here,
  first,
  last,
  associations,
}: {
  stop: Stop
  passed: boolean
  here?: boolean
  first: boolean
  last: boolean
  associations: Association[]
}) {
  const pass = stop.kind === 'pass'
  const t = stop.departure ?? stop.arrival ?? stop.pass
  const late = (stop.departure?.actual ? stop.departure : stop.arrival?.actual ? stop.arrival : stop.pass ?? t)?.delayMinutes
  const aspect = aspectFor(late, { cancelled: stop.cancelled, live: !!(t?.actual || t?.estimated) })
  return (
    <li
      className={[
        'stop',
        pass && 'stop-pass',
        passed && 'stop-passed',
        here && 'stop-here',
        stop.cancelled && 'stop-cancelled',
        first && 'stop-first',
        last && 'stop-last',
      ]
        .filter(Boolean)
        .join(' ')}
    >
      <span className="stop-times">
        {pass ? (
          <TimeCell t={stop.pass} label="pass" />
        ) : (
          <>
            {!first && <TimeCell t={stop.arrival} label="arr" />}
            {!last && <TimeCell t={stop.departure} label="dep" />}
          </>
        )}
      </span>
      <span className="stop-line" aria-hidden="true">
        <span className="stop-node" />
      </span>
      <span className="stop-body">
        <span className="stop-name">
          {stop.crs ? <Link to={`/station/${stop.crs}`}>{stop.name}</Link> : stop.name}
          {here && <span className="stop-flag">{stop.atPlatform ? 'At platform' : stop.approaching ? 'Approaching' : 'Last reported here'}</span>}
        </span>
        <span className="stop-notes">
          {!pass && (t?.actual || t?.estimated) && <Signal aspect={aspect} />}
          {!pass && (t?.actual || t?.estimated) && <span>{lateness(late) ?? 'On time'}</span>}
          {pass && lateness(late) && <span>{lateness(late)}</span>}
          {stop.startsHere && <span>Starts here</span>}
          {stop.terminatesHere && <span>Terminates here</span>}
          {stop.cancelled && <span>Cancelled at this stop</span>}
          {stop.kind === 'stop' && <span>Stops for operational reasons only</span>}
        </span>
        {associations.map(a => (
          <span key={`${a.type}-${a.service.uid}`} className={`assoc${a.cancelled ? ' assoc-cancelled' : ''}`}>
            {associationText[a.type]}{' '}
            <Link to={`/train/${a.service.uid}/${a.service.runDate}`}>
              {a.service.headcode ?? a.service.uid} to {a.service.destination[0]?.name ?? 'its destination'}
            </Link>
            {a.cancelled && ' (cancelled)'}
          </span>
        ))}
      </span>
      {!pass && <PlatformSign stop={stop} />}
    </li>
  )
}

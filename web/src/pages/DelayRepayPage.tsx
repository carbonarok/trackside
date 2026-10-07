import { useEffect, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { api, type DelayRepay, type Location } from '../api'
import { StationPicker } from '../components/Search'
import { Signal } from '../components/ui'
import { aspectFor, hhmm, longDate, ukToday } from '../format'

export function DelayRepayPage() {
  const [params] = useSearchParams()
  const [from, setFrom] = useState<Location>()
  const [to, setTo] = useState<Location>()
  const [date, setDate] = useState(params.get('date') ?? ukToday())
  const [departure, setDeparture] = useState(params.get('departure') ?? '')
  const [result, setResult] = useState<DelayRepay>()
  const [error, setError] = useState<string>()
  const [checking, setChecking] = useState(false)

  useEffect(() => {
    document.title = 'Delay Repay checker · trackside'
    // Prefill stations from a link such as a train page's.
    for (const [key, set] of [
      ['from', setFrom],
      ['to', setTo],
    ] as const) {
      const code = params.get(key)
      if (code) {
        api
          .searchLocations(code)
          .then(ls => set(ls.find(l => l.crs === code.toUpperCase()) ?? ls[0]))
          .catch(() => {})
      }
    }
  }, [params])

  const check = async (e: { preventDefault(): void }) => {
    e.preventDefault()
    if (!from?.crs || !to?.crs || !departure) {
      setError('Choose both stations and the booked departure time.')
      return
    }
    setChecking(true)
    setError(undefined)
    setResult(undefined)
    try {
      setResult(await api.delayRepay({ from: from.crs, to: to.crs, date, departure }))
    } catch (err) {
      setError((err as Error).message)
    } finally {
      setChecking(false)
    }
  }

  return (
    <div className="page repay-page">
      <header className="table-head">
        <h1>Delay Repay checker</h1>
        <p className="lede">
          Find out how late your train got you there, measured the way Delay Repay is: arrival at your destination against
          the booked time. If your train was cancelled, it uses the next train that ran.
        </p>
      </header>
      <form className="repay-form" onSubmit={check}>
        <StationPicker label="From" value={from} onChange={setFrom} />
        <StationPicker label="To" value={to} onChange={setTo} />
        <div className="field">
          <label htmlFor="date">Date</label>
          <input id="date" type="date" value={date} max={ukToday()} onChange={e => setDate(e.target.value)} />
        </div>
        <div className="field">
          <label htmlFor="dep">Booked departure</label>
          <input id="dep" type="time" value={departure} onChange={e => setDeparture(e.target.value)} />
        </div>
        <button className="button" type="submit" disabled={checking}>
          {checking ? 'Checking…' : 'Check my journey'}
        </button>
      </form>

      {error && <p className="note note-error">{error}</p>}
      {result && <RepayResult r={result} />}
    </div>
  )
}

function RepayResult({ r }: { r: DelayRepay }) {
  const aspect = aspectFor(r.delayMinutes, { cancelled: r.cancelled && !r.arrivedAt, live: r.delayMinutes !== undefined })
  return (
    <section className={`repay-result${r.eligible ? ' repay-eligible' : ''}`} aria-live="polite">
      <h2>
        <Signal aspect={aspect} />
        {r.delayMinutes === undefined
          ? 'No arrival reported yet'
          : r.delayMinutes <= 0
            ? 'You arrived on time'
            : `You arrived ${r.delayMinutes} minutes late`}
      </h2>
      {r.eligible && r.compensation && (
        <p className="repay-claim">
          That’s the {r.band} minute band: usually {r.compensation.singlePercent}% of a single fare or{' '}
          {r.compensation.returnPercent}% of a return.
        </p>
      )}
      <dl>
        <dt>Train</dt>
        <dd>
          <Link to={`/train/${r.train.uid}/${r.train.runDate}`}>
            {hhmm(r.bookedDeparture)} {r.from.name} to {r.to.name}
          </Link>
          , {longDate(r.train.runDate)}
          {r.train.operator && `, ${r.train.operator.name}`}
        </dd>
        <dt>Booked arrival</dt>
        <dd>{hhmm(r.bookedArrival)}</dd>
        {r.cancelled && (
          <>
            <dt>Cancelled</dt>
            <dd>
              {r.cancelReason ?? 'Yes'}
              {r.usedTrain && (
                <>
                  . Next train:{' '}
                  <Link to={`/train/${r.usedTrain.uid}/${r.usedTrain.runDate}`}>{r.usedTrain.headcode ?? r.usedTrain.uid}</Link>
                </>
              )}
            </dd>
          </>
        )}
        {r.arrivedAt && (
          <>
            <dt>Actual arrival</dt>
            <dd>
              {hhmm(r.arrivedAt)}
              {r.arrivalSource && <span className="muted"> (reported by {r.arrivalSource})</span>}
            </dd>
          </>
        )}
        {r.lateReason && (
          <>
            <dt>Reason given</dt>
            <dd>{r.lateReason}</dd>
          </>
        )}
      </dl>
      {/* With no arrival the heading already says what the note would. */}
      {(r.delayMinutes !== undefined || r.cancelled) && <p className="muted">{r.note}</p>}
    </section>
  )
}

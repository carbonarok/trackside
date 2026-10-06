import { useEffect, useId, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api, type Location, type ServiceSummary } from '../api'
import { hhmm, ukToday } from '../format'

type Result =
  | { kind: 'station'; location: Location }
  | { kind: 'train'; service: ServiceSummary }

const trainLike = /^(\d[a-z]\d\d|[a-z]\d{5})$/i

/**
 * One box for stations ("Clapham", "CLJ") and today's trains (headcode
 * "2P47" or UID "W12345"). Arrow keys move through results; Enter opens one.
 */
export function Search({ autoFocus, placeholder = 'Station, headcode or train ID' }: { autoFocus?: boolean; placeholder?: string }) {
  const [q, setQ] = useState('')
  const [results, setResults] = useState<Result[]>([])
  const [active, setActive] = useState(0)
  const [open, setOpen] = useState(false)
  const [searched, setSearched] = useState(false)
  const navigate = useNavigate()
  const listId = useId()
  const box = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const term = q.trim()
    if (term.length < 2) {
      setResults([])
      setSearched(false)
      return
    }
    const ctrl = new AbortController()
    const t = window.setTimeout(async () => {
      try {
        const [locations, services] = await Promise.all([
          api.searchLocations(term, ctrl.signal).catch(() => []),
          trainLike.test(term) ? api.searchServices(term, ukToday(), ctrl.signal).catch(() => []) : Promise.resolve([]),
        ])
        setResults([
          ...services.map(s => ({ kind: 'train' as const, service: s })),
          ...locations.filter(l => l.crs).slice(0, 8).map(l => ({ kind: 'station' as const, location: l })),
        ])
        setActive(0)
        setSearched(true)
      } catch {
        // superseded by a newer search
      }
    }, 180)
    return () => {
      window.clearTimeout(t)
      ctrl.abort()
    }
  }, [q])

  useEffect(() => {
    const close = (e: MouseEvent) => {
      if (!box.current?.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('mousedown', close)
    return () => document.removeEventListener('mousedown', close)
  }, [])

  const go = (r: Result) => {
    setOpen(false)
    setQ('')
    if (r.kind === 'station') navigate(`/station/${r.location.crs}`)
    else navigate(`/train/${r.service.uid}/${r.service.runDate}`)
  }

  const onKey = (e: React.KeyboardEvent) => {
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      setOpen(true)
      setActive(a => Math.min(a + 1, results.length - 1))
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      setActive(a => Math.max(a - 1, 0))
    } else if (e.key === 'Enter' && results[active]) {
      e.preventDefault()
      go(results[active])
    } else if (e.key === 'Escape') {
      setOpen(false)
    }
  }

  const showList = open && q.trim().length >= 2 && searched
  return (
    <div className="search" ref={box}>
      <input
        type="search"
        role="combobox"
        aria-expanded={showList}
        aria-controls={listId}
        aria-activedescendant={showList && results[active] ? `${listId}-${active}` : undefined}
        aria-label="Search stations and trains"
        placeholder={placeholder}
        value={q}
        autoFocus={autoFocus}
        onChange={e => {
          setQ(e.target.value)
          setOpen(true)
        }}
        onFocus={() => setOpen(true)}
        onKeyDown={onKey}
      />
      {showList && (
        <ul className="search-results" id={listId} role="listbox">
          {results.length === 0 && <li className="search-empty">No stations or trains today match “{q.trim()}”.</li>}
          {results.map((r, i) => (
            <li
              key={r.kind === 'station' ? `s-${r.location.tiploc}` : `t-${r.service.uid}-${r.service.runDate}`}
              id={`${listId}-${i}`}
              role="option"
              aria-selected={i === active}
              className={i === active ? 'active' : undefined}
              onMouseEnter={() => setActive(i)}
              onMouseDown={e => {
                e.preventDefault()
                go(r)
              }}
            >
              {r.kind === 'station' ? (
                <>
                  <span className="result-main">{r.location.name}</span>
                  <span className="result-code">{r.location.crs}</span>
                </>
              ) : (
                <>
                  <span className="result-main">
                    {hhmm(r.service.origin[0]?.time)} {r.service.origin[0]?.name} to {r.service.destination[0]?.name}
                  </span>
                  <span className="result-code">{r.service.headcode}</span>
                </>
              )}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

/** Picks a single station, for forms and filters. */
export function StationPicker({
  label,
  value,
  onChange,
}: {
  label: string
  value?: Location
  onChange: (l: Location | undefined) => void
}) {
  const [q, setQ] = useState(value?.name ?? '')
  const [options, setOptions] = useState<Location[]>([])
  const [open, setOpen] = useState(false)
  const id = useId()

  useEffect(() => setQ(value?.name ?? ''), [value])

  useEffect(() => {
    if (!open || q.trim().length < 2 || q === value?.name) return
    const ctrl = new AbortController()
    const t = window.setTimeout(() => {
      api
        .searchLocations(q.trim(), ctrl.signal)
        .then(ls => setOptions(ls.filter(l => l.crs).slice(0, 8)))
        .catch(() => {})
    }, 180)
    return () => {
      window.clearTimeout(t)
      ctrl.abort()
    }
  }, [q, open, value])

  return (
    <div className="field picker">
      <label htmlFor={id}>{label}</label>
      <input
        id={id}
        type="search"
        autoComplete="off"
        value={q}
        placeholder="Station name or code"
        onChange={e => {
          setQ(e.target.value)
          setOpen(true)
          if (!e.target.value) onChange(undefined)
        }}
        onBlur={() => window.setTimeout(() => setOpen(false), 120)}
      />
      {open && options.length > 0 && (
        <ul className="search-results" role="listbox">
          {options.map(l => (
            <li
              key={l.tiploc}
              role="option"
              aria-selected={false}
              onMouseDown={e => {
                e.preventDefault()
                onChange(l)
                setQ(l.name)
                setOpen(false)
              }}
            >
              <span className="result-main">{l.name}</span>
              <span className="result-code">{l.crs}</span>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

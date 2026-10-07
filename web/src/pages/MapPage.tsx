import L from 'leaflet'
import 'leaflet/dist/leaflet.css'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { MapContainer, TileLayer, useMap } from 'react-leaflet'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import { api, ApiError, type TrainMap, type TrainPosition } from '../api'
import { Signal, usePolling } from '../components/ui'
import { aspectFor, aspectLabel, hhmm, lateness, type Aspect } from '../format'

// The server can point the map at other tiles (MAP_TILE_URL); OpenStreetMap's
// own servers are fine for light use only.
const config = (window as { trackside?: { tileUrl?: string; tileAttribution?: string } }).trackside ?? {}
const tileUrl = config.tileUrl || 'https://tile.openstreetmap.org/{z}/{x}/{y}.png'
const tileAttribution = [
  config.tileUrl
    ? config.tileAttribution
    : '&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors',
  'Contains public sector information licensed under the Open Government Licence v3.0',
  'Powered by National Rail Enquiries',
]
  .filter(Boolean)
  .join('. ')

const POLL_MS = 15_000
const MATCH_LIMIT = 8

const keyOf = (p: TrainPosition) => `${p.uid}|${p.runDate}`
const aspectOf = (p: TrainPosition) => aspectFor(p.delayMinutes, { live: p.live })
const plural = (n: number, one: string, many: string) => `${n.toLocaleString('en-GB')} ${n === 1 ? one : many}`
const reducedMotion = () => window.matchMedia('(prefers-reduced-motion: reduce)').matches

function describe(error: Error) {
  if (error instanceof ApiError) return error.message
  return 'Can’t reach the trackside server.'
}

/**
 * A train drawn as an arrow pointing the way it's going, or a dot when it's
 * at a station. Colours come from the aspect tokens in styles.css. Double
 * yellow carries a signal-head bar across the middle, splitting the mark
 * into two yellow lamps.
 */
function markerSvg(aspect: Aspect, atStation: boolean, bearing = 0) {
  const outline = atStation ? '<circle cx="9" cy="9" r="6"/>' : '<path d="M9 1 L16 16 L9 12 L2 16 Z"/>'
  const bar =
    aspect !== 'double'
      ? ''
      : atStation
        ? '<rect class="m-bar" x="3.2" y="7.75" width="11.6" height="2.5"/>'
        : '<path class="m-bar" d="M5.7 8 L12.3 8 L13.4 10.5 L4.6 10.5 Z"/>'
  const body = `<g class="m-fill">${outline}</g>${bar}<g class="m-ring">${outline}</g>`
  const shape = atStation ? body : `<g transform="rotate(${bearing} 9 9)">${body}</g>`
  return `<svg viewBox="0 0 18 18" class="m m-${aspect}" aria-hidden="true">${shape}</svg>`
}

// Several thousand markers share a few hundred distinct looks.
const iconCache = new Map<string, L.DivIcon>()

function markerIcon(p: TrainPosition, zoom: number, selected: boolean) {
  const size = (zoom >= 14 ? 28 : zoom >= 12 ? 22 : zoom >= 9 ? 18 : 14) + (selected ? 8 : 0)
  const aspect = aspectOf(p)
  const bearing = p.atStation ? 0 : Math.round(p.bearing / 5) * 5
  const sig = `${aspect}|${p.atStation}|${bearing}|${size}|${selected}`
  let icon = iconCache.get(sig)
  if (!icon) {
    if (iconCache.size > 4000) iconCache.clear()
    icon = L.divIcon({
      className: `train-marker${selected ? ' is-selected' : ''}`,
      html: markerSvg(aspect, p.atStation, bearing).replace('<svg ', `<svg width="${size}" height="${size}" `),
      iconSize: [size, size],
      iconAnchor: [size / 2, size / 2],
    })
    iconCache.set(sig, icon)
  }
  return icon
}

function matches(p: TrainPosition, q: string) {
  if (!q) return true
  const s = q.toLowerCase()
  return [p.headcode, p.uid, p.operator?.name, p.origin, p.destination, p.last?.name, p.next?.name].some(v =>
    v?.toLowerCase().includes(s),
  )
}

/** Where the train is, in words. */
function whereText(p: TrainPosition) {
  if (p.atStation && p.last) return `At ${p.last.name}`
  if (p.last && p.next) return `Between ${p.last.name} and ${p.next.name}`
  if (p.last) return `Last reported at ${p.last.name}`
  if (p.next) return `Next stop ${p.next.name}`
  return 'Position estimated from the timetable'
}

/** Keeps one Leaflet marker per train, updated in place on each refresh. */
function TrainLayer({
  trains,
  selected,
  onSelect,
}: {
  trains: TrainMap['features']
  selected?: string
  onSelect: (key: string) => void
}) {
  const map = useMap()
  const markers = useRef(new Map<string, L.Marker>())
  const [zoom, setZoom] = useState(map.getZoom())

  useEffect(() => {
    const onZoom = () => setZoom(map.getZoom())
    map.on('zoomend', onZoom)
    return () => {
      map.off('zoomend', onZoom)
    }
  }, [map])

  useEffect(() => {
    const seen = new Set<string>()
    for (const f of trains) {
      const p = f.properties
      const key = keyOf(p)
      seen.add(key)
      const ll: L.LatLngExpression = [f.geometry.coordinates[1], f.geometry.coordinates[0]]
      const icon = markerIcon(p, zoom, key === selected)
      const label = p.headcode ?? p.uid
      let m = markers.current.get(key)
      if (!m) {
        m = L.marker(ll, { icon, keyboard: false, riseOnHover: true })
          .bindTooltip(label, { direction: 'top', offset: [0, -8] })
          .on('click', () => onSelect(key))
          .addTo(map)
        markers.current.set(key, m)
      } else {
        m.setLatLng(ll)
        // Replacing an icon rebuilds its DOM; skip it when nothing changed.
        if (m.options.icon !== icon) m.setIcon(icon)
        if (m.getTooltip()?.getContent() !== label) m.setTooltipContent(label)
      }
      m.setZIndexOffset(key === selected ? 1000 : 0)
    }
    for (const [key, m] of markers.current) {
      if (!seen.has(key)) {
        m.remove()
        markers.current.delete(key)
      }
    }
  }, [trains, zoom, selected, map, onSelect])

  useEffect(() => {
    const current = markers.current
    return () => {
      for (const m of current.values()) m.remove()
      current.clear()
    }
  }, [])
  return null
}

/**
 * Stations appear once zoomed in far enough to read them. They're fetched the
 * first time that happens, and fetched again on the next zoom if that failed.
 */
function StationLayer() {
  const map = useMap()
  const navigate = useNavigate()
  useEffect(() => {
    const group = L.layerGroup()
    let ctrl: AbortController | undefined
    let state: 'idle' | 'loading' | 'loaded' = 'idle'
    const load = () => {
      state = 'loading'
      ctrl = new AbortController()
      api
        .stations(ctrl.signal)
        .then(data => {
          for (const f of data.features) {
            const [lon, lat] = f.geometry.coordinates
            L.circleMarker([lat, lon], { radius: 3.5, weight: 1.5, className: 'station-dot' })
              .bindTooltip(f.properties.name)
              .on('click', () => navigate(`/station/${f.properties.crs}`))
              .addTo(group)
          }
          state = 'loaded'
        })
        .catch(() => {
          state = 'idle'
        })
    }
    const toggle = () => {
      if (map.getZoom() < 10) return group.remove()
      if (state === 'idle') load()
      group.addTo(map)
    }
    map.on('zoomend', toggle)
    toggle()
    return () => {
      ctrl?.abort()
      map.off('zoomend', toggle)
      group.remove()
    }
  }, [map, navigate])
  return null
}

const britain: L.LatLngBoundsExpression = [
  [49.9, -6.4],
  [58.7, 1.8],
]

const clamp = (v: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, v))

/**
 * Opens on Great Britain, or on the view in the URL (#zoom/lat/lon), and
 * keeps the URL up to date so a view can be bookmarked or shared.
 */
function ViewInHash() {
  const map = useMap()
  useEffect(() => {
    const fromHash = () => {
      const [z, lat, lon] = location.hash.slice(1).split('/').map(Number)
      if (![z, lat, lon].every(Number.isFinite)) return false
      map.setView([clamp(lat, -85, 85), clamp(lon, -180, 180)], clamp(z, 3, 18))
      return true
    }
    map.invalidateSize()
    if (!fromHash()) map.fitBounds(britain)
    // A hash typed or pasted into an open map; replaceState below doesn't fire this.
    window.addEventListener('hashchange', fromHash)
    const save = () => {
      const c = map.getCenter()
      history.replaceState(history.state, '', `${location.pathname}${location.search}#${map.getZoom()}/${c.lat.toFixed(4)}/${c.lng.toFixed(4)}`)
    }
    map.on('moveend', save)
    return () => {
      map.off('moveend', save)
      window.removeEventListener('hashchange', fromHash)
    }
  }, [map])
  return null
}

/** Flies to the selected train the first time it appears. */
function FollowSelected({ trains, selected }: { trains: TrainMap['features']; selected?: string }) {
  const map = useMap()
  const done = useRef<string | undefined>(undefined)
  useEffect(() => {
    if (!selected || done.current === selected) return
    const f = trains.find(t => keyOf(t.properties) === selected)
    if (!f) return
    done.current = selected
    const at: L.LatLngExpression = [f.geometry.coordinates[1], f.geometry.coordinates[0]]
    const zoom = Math.max(map.getZoom(), 12)
    if (reducedMotion()) map.setView(at, zoom)
    else map.flyTo(at, zoom, { duration: 0.8 })
  }, [trains, selected, map])
  return null
}

export function MapPage() {
  const [params, setParams] = useSearchParams()
  const [all, setAll] = useState(false)
  const [filter, setFilter] = useState('')
  const [tilesFailing, setTilesFailing] = useState(false)
  const selected = params.get('train') ?? undefined
  // Positions move all the time, so a pushed change refetches at most every 10 s.
  const { data, error } = usePolling(signal => api.trains(all, signal), [all], POLL_MS, { topic: 'map', minGapMs: 10_000 })
  const filterRef = useRef<HTMLInputElement>(null)
  const headingRef = useRef<HTMLHeadingElement>(null)
  const focusTrain = useRef(false)
  const lastTileLoad = useRef(0)

  useEffect(() => {
    document.title = 'Live train map · trackside'
  }, [])

  const visible = useMemo(() => (data?.features ?? []).filter(f => matches(f.properties, filter)), [data, filter])
  const live = useMemo(() => visible.filter(f => f.properties.live).length, [visible])
  const train = data?.features.find(f => keyOf(f.properties) === selected)?.properties

  const select = useCallback(
    (key: string) => {
      focusTrain.current = Boolean(key)
      setParams(
        prev => {
          const next = new URLSearchParams(prev)
          if (key) next.set('train', key)
          else next.delete('train')
          return next
        },
        { replace: true },
      )
    },
    [setParams],
  )

  const close = () => {
    select('')
    filterRef.current?.focus()
  }

  // A train picked on the map or from the list takes focus, so keyboard and
  // screen reader users land on what they chose.
  useEffect(() => {
    if (train && focusTrain.current) {
      focusTrain.current = false
      headingRef.current?.focus({ preventScroll: true })
    }
  }, [train])

  const tileEvents = useMemo(
    () => ({
      tileload: () => {
        lastTileLoad.current = Date.now()
        setTilesFailing(false)
      },
      // Odd tiles fail at the edges of the world; only warn when none are arriving.
      tileerror: () => {
        if (Date.now() - lastTileLoad.current > 10_000) setTilesFailing(true)
      },
    }),
    [],
  )

  let status = ''
  if (!data && !error) status = 'Loading trains…'
  else if (!data && error) status = `Couldn’t load trains. ${describe(error)} Trying again every ${POLL_MS / 1000} seconds.`
  else if (data && error) status = `Couldn’t update. ${describe(error)} Showing positions from ${hhmm(data.generatedAt)}.`
  else if (data)
    status = `${plural(visible.length, 'train', 'trains')}, ${live.toLocaleString('en-GB')} reporting live. Updated ${hhmm(data.generatedAt)}.`

  const matchList = filter && visible.length > 0 ? visible.slice(0, MATCH_LIMIT) : []

  return (
    <div className="map-page">
      <MapContainer center={[54.3, -2.3]} zoom={6} zoomSnap={0.5} zoomControl={false} className="map">
        <ViewInHash />
        <TileLayer url={tileUrl} maxZoom={18} attribution={tileAttribution} eventHandlers={tileEvents} />
        <TrainLayer trains={visible} selected={selected} onSelect={select} />
        <StationLayer />
        <FollowSelected trains={visible} selected={selected} />
      </MapContainer>

      <aside
        className="map-panel"
        aria-label="Trains on the map"
        onKeyDown={e => {
          if (e.key === 'Escape' && selected) close()
        }}
      >
        <input
          ref={filterRef}
          type="search"
          placeholder="Filter by headcode, operator or station"
          aria-label="Filter trains"
          aria-describedby="map-status"
          value={filter}
          onChange={e => setFilter(e.target.value)}
        />
        <p id="map-status" className={`map-count${error ? ' map-count-error' : ''}`}>
          {status}
        </p>
        <label className="toggle">
          <input type="checkbox" checked={all} onChange={e => setAll(e.target.checked)} /> Include freight and empty stock
        </label>
        {tilesFailing && (
          <p className="map-note" role="status">
            The map background isn’t loading. Train positions still update.
          </p>
        )}

        {train ? (
          <div className="map-train">
            <p className="service-meta">
              {train.headcode && <span className="headcode">{train.headcode}</span>}
              <span>{train.operator?.name ?? 'Operator not known'}</span>
            </p>
            <h2 ref={headingRef} tabIndex={-1}>
              {train.origin} to {train.destination}
            </h2>
            <p className="map-where">
              <Signal aspect={aspectOf(train)} />
              <span>
                {whereText(train)}
                <br />
                <strong>{train.live ? (lateness(train.delayMinutes) ?? 'On time') : 'Nothing reported yet'}</strong>
              </span>
            </p>
            <div className="actions">
              <Link className="button" to={`/train/${train.uid}/${train.runDate}`}>
                Open this train
              </Link>
              <button className="button button-quiet" onClick={close}>
                Close
              </button>
            </div>
          </div>
        ) : selected && data ? (
          <MissingTrain trainKey={selected} onClose={close} />
        ) : data && data.features.length === 0 ? (
          <p className="map-empty">
            No trains to show. Trains appear here once the live movements feed reports them.
          </p>
        ) : filter && data && visible.length === 0 ? (
          <div className="map-empty">
            <p>No trains match “{filter}”.</p>
            <button className="link-button" onClick={() => setFilter('')}>
              Clear the filter
            </button>
          </div>
        ) : matchList.length > 0 ? (
          <div className="map-matches">
            <ul>
              {matchList.map(f => {
                const p = f.properties
                return (
                  <li key={keyOf(p)}>
                    <button onClick={() => select(keyOf(p))}>
                      <Signal aspect={aspectOf(p)} />
                      {p.headcode && <span className="headcode">{p.headcode}</span>}
                      <span className="map-match-route">
                        {p.origin} to {p.destination}
                      </span>
                    </button>
                  </li>
                )
              })}
            </ul>
            {visible.length > MATCH_LIMIT && (
              <p className="map-more">
                {plural(visible.length - MATCH_LIMIT, 'more train', 'more trains')} match. Add to the filter to narrow it down.
              </p>
            )}
          </div>
        ) : (
          <div className="legend">
            <ul>
              {(['green', 'yellow', 'double', 'red', 'unlit'] as Aspect[]).map(a => (
                <li key={a}>
                  <span className="legend-mark" dangerouslySetInnerHTML={{ __html: markerSvg(a, false) }} />
                  {aspectLabel[a]}
                </li>
              ))}
            </ul>
            <p className="legend-note">Arrows point the way the train is going. A dot is a train at a station.</p>
          </div>
        )}
      </aside>
    </div>
  )
}

/** A linked train that isn't on the map: finished, not started, or not reporting. */
function MissingTrain({ trainKey, onClose }: { trainKey: string; onClose: () => void }) {
  const [uid, runDate] = trainKey.split('|')
  return (
    <div className="map-train">
      <h2>This train isn’t on the map now</h2>
      <p className="map-where">It may have finished its journey, not started yet, or not reported where it is.</p>
      <div className="actions">
        {uid && runDate && (
          <Link className="button" to={`/train/${encodeURIComponent(uid)}/${runDate}`}>
            Open this train
          </Link>
        )}
        <button className="button button-quiet" onClick={onClose}>
          Close
        </button>
      </div>
    </div>
  )
}

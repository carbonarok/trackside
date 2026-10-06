import L from 'leaflet'
import 'leaflet/dist/leaflet.css'
import { useEffect, useMemo, useRef, useState } from 'react'
import { MapContainer, TileLayer, useMap } from 'react-leaflet'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import { api, type TrainMap, type TrainPosition } from '../api'
import { Signal, usePolling } from '../components/ui'
import { aspectFor, aspectLabel, lateness, type Aspect } from '../format'

const aspectColour: Record<Aspect, string> = {
  green: '#2e9e5b',
  yellow: '#e3b20f',
  double: '#e5761a',
  red: '#d33a2c',
  unlit: '#8b93a1',
}

// The server can point the map at other tiles (MAP_TILE_URL); OpenStreetMap's
// own servers are fine for light use only.
const config = (window as { trackside?: { tileUrl?: string; tileAttribution?: string } }).trackside ?? {}
const tileUrl = config.tileUrl || 'https://tile.openstreetmap.org/{z}/{x}/{y}.png'
const tileAttribution =
  (config.tileUrl
    ? config.tileAttribution ?? ''
    : '&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors') +
  '. Contains public sector information licensed under the Open Government Licence v3.0. Powered by National Rail Enquiries.'

const keyOf = (p: TrainPosition) => `${p.uid}|${p.runDate}`
const aspectOf = (p: TrainPosition) => aspectFor(p.delayMinutes, { live: p.live })

function markerIcon(p: TrainPosition, zoom: number, selected: boolean) {
  const size = (zoom >= 14 ? 28 : zoom >= 12 ? 22 : zoom >= 9 ? 18 : 14) + (selected ? 8 : 0)
  const c = aspectColour[aspectOf(p)]
  const ring = selected ? '#14275a' : '#fff'
  const shape = p.atStation
    ? `<circle cx="9" cy="9" r="6" fill="${c}" stroke="${ring}" stroke-width="2"/>`
    : `<g transform="rotate(${p.bearing} 9 9)"><path d="M9 1 L16 16 L9 12 L2 16 Z" fill="${c}" stroke="${ring}" stroke-width="1.6" stroke-linejoin="round"/></g>`
  return L.divIcon({
    className: 'train-marker',
    html: `<svg viewBox="0 0 18 18" width="${size}" height="${size}">${shape}</svg>`,
    iconSize: [size, size],
    iconAnchor: [size / 2, size / 2],
  })
}

function matches(p: TrainPosition, q: string) {
  if (!q) return true
  const s = q.toLowerCase()
  return [p.headcode, p.uid, p.operator?.name, p.origin, p.destination, p.last?.name, p.next?.name].some(v =>
    v?.toLowerCase().includes(s),
  )
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
      let m = markers.current.get(key)
      if (!m) {
        m = L.marker(ll, { icon, keyboard: false, riseOnHover: true })
          .bindTooltip(p.headcode ?? p.uid, { direction: 'top', offset: [0, -8] })
          .on('click', () => onSelect(key))
          .addTo(map)
        markers.current.set(key, m)
      } else {
        m.setLatLng(ll).setIcon(icon)
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

/** Stations appear once zoomed in far enough to read them. */
function StationLayer() {
  const map = useMap()
  const navigate = useNavigate()
  useEffect(() => {
    const group = L.layerGroup()
    const ctrl = new AbortController()
    api
      .stations(ctrl.signal)
      .then(data => {
        for (const f of data.features) {
          const [lon, lat] = f.geometry.coordinates
          L.circleMarker([lat, lon], { radius: 3.5, weight: 1.5, color: '#14275a', fillColor: '#fff', fillOpacity: 1 })
            .bindTooltip(f.properties.name)
            .on('click', () => navigate(`/station/${f.properties.crs}`))
            .addTo(group)
        }
      })
      .catch(() => {})
    const toggle = () => (map.getZoom() >= 10 ? group.addTo(map) : group.remove())
    map.on('zoomend', toggle)
    toggle()
    return () => {
      ctrl.abort()
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
      map.setView([lat, lon], z)
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
    map.flyTo([f.geometry.coordinates[1], f.geometry.coordinates[0]], Math.max(map.getZoom(), 12), { duration: 0.8 })
  }, [trains, selected, map])
  return null
}

export function MapPage() {
  const [params, setParams] = useSearchParams()
  const [all, setAll] = useState(false)
  const [filter, setFilter] = useState('')
  const selected = params.get('train') ?? undefined
  const { data, error } = usePolling(signal => api.trains(all, signal), [all], 15_000)

  useEffect(() => {
    document.title = 'Live train map · trackside'
  }, [])

  const visible = useMemo(() => (data?.features ?? []).filter(f => matches(f.properties, filter)), [data, filter])
  const live = useMemo(() => visible.filter(f => f.properties.live).length, [visible])
  const train = data?.features.find(f => keyOf(f.properties) === selected)?.properties
  const select = useMemo(
    () => (key: string) => setParams(key ? { train: key } : {}, { replace: true }),
    [setParams],
  )

  return (
    <div className="map-page">
      <MapContainer center={[54.3, -2.3]} zoom={6} zoomSnap={0.5} zoomControl={false} className="map">
        <ViewInHash />
        <TileLayer url={tileUrl} maxZoom={18} attribution={tileAttribution} />
        <TrainLayer trains={visible} selected={selected} onSelect={select} />
        <StationLayer />
        <FollowSelected trains={visible} selected={selected} />
      </MapContainer>

      <aside className="map-panel">
        <input
          type="search"
          placeholder="Filter by headcode, operator or station"
          aria-label="Filter trains"
          value={filter}
          onChange={e => setFilter(e.target.value)}
        />
        <p className="map-count">
          {data ? `${visible.length.toLocaleString('en-GB')} trains, ${live.toLocaleString('en-GB')} reporting live` : error ? error.message : 'Loading trains…'}
        </p>
        <label className="toggle">
          <input type="checkbox" checked={all} onChange={e => setAll(e.target.checked)} /> Include freight and empty stock
        </label>

        {train ? (
          <div className="map-train">
            <p className="service-meta">
              {train.headcode && <span className="headcode">{train.headcode}</span>}
              <span>{train.operator?.name ?? 'Operator not known'}</span>
            </p>
            <h2>
              {train.origin} to {train.destination}
            </h2>
            <p className="map-where">
              <Signal aspect={aspectOf(train)} />
              <span>
                {train.atStation && train.last
                  ? `At ${train.last.name}`
                  : train.last && train.next
                    ? `Between ${train.last.name} and ${train.next.name}`
                    : ''}
                <br />
                <strong>{train.live ? (lateness(train.delayMinutes) ?? 'On time') : 'Nothing reported yet'}</strong>
              </span>
            </p>
            <div className="actions">
              <Link className="button" to={`/train/${train.uid}/${train.runDate}`}>
                Open this train
              </Link>
              <button className="button button-quiet" onClick={() => select('')}>
                Close
              </button>
            </div>
          </div>
        ) : (
          <ul className="legend">
            {(['green', 'yellow', 'double', 'red', 'unlit'] as Aspect[]).map(a => (
              <li key={a}>
                <Signal aspect={a} /> {aspectLabel[a]}
              </li>
            ))}
          </ul>
        )}
      </aside>
    </div>
  )
}

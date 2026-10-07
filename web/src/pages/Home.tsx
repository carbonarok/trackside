import { Link } from 'react-router-dom'
import { api } from '../api'
import { Flap } from '../components/Flap'
import { Search } from '../components/Search'
import { usePolling } from '../components/ui'

const busy = [
  ['London Waterloo', 'WAT'],
  ['Clapham Junction', 'CLJ'],
  ['London Paddington', 'PAD'],
  ['Birmingham New Street', 'BHM'],
  ['Manchester Piccadilly', 'MAN'],
  ['Leeds', 'LDS'],
  ['Edinburgh', 'EDB'],
  ['Glasgow Central', 'GLC'],
  ['Bristol Temple Meads', 'BRI'],
  ['Reading', 'RDG'],
]

export function Home() {
  const { data } = usePolling(signal => api.trains(false, signal), [], 60_000)
  const running = data?.features.length
  return (
    <div className="home">
      <section className="home-hero">
        <h1>Live train times for Great Britain</h1>
        <p className="lede">Departures, arrivals and every train’s journey, from Network Rail and National Rail open data.</p>
        <Search autoFocus />
        {running !== undefined && running > 0 && (
          <p className="running">
            <Flap className="running-count" value={running.toLocaleString('en-GB')} introduce />
            <span>passenger trains are running right now.</span>
            <Link to="/map">See them on the map</Link>
          </p>
        )}
      </section>
      <section className="home-stations" aria-labelledby="busy-heading">
        <h2 id="busy-heading">Busy stations</h2>
        <ul>
          {busy.map(([name, crs]) => (
            <li key={crs}>
              <Link to={`/station/${crs}`}>
                {name} <span className="code">{crs}</span>
              </Link>
            </li>
          ))}
        </ul>
      </section>
    </div>
  )
}

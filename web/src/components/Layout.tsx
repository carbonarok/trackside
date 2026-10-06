import { NavLink, Outlet, useLocation } from 'react-router-dom'
import { Search } from './Search'

export function Layout() {
  const { pathname } = useLocation()
  const home = pathname === '/'
  const full = pathname.startsWith('/map')
  return (
    <div className={`app${full ? ' app-full' : ''}`}>
      <header className="topbar">
        <NavLink to="/" className="brand" aria-label="trackside home">
          <svg viewBox="0 0 20 32" aria-hidden="true" className="brand-mark">
            <rect x="2" y="1" width="16" height="30" rx="8" />
            <circle cx="10" cy="10" r="4.2" className="b-green" />
            <circle cx="10" cy="22" r="4.2" className="b-off" />
          </svg>
          trackside
        </NavLink>
        <nav>
          <NavLink to="/map">Live map</NavLink>
          <NavLink to="/delay-repay">Delay Repay</NavLink>
        </nav>
        {!home && <div className="topbar-search"><Search /></div>}
      </header>
      <main>
        <Outlet />
      </main>
    </div>
  )
}

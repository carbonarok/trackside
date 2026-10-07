import { NavLink, Outlet, useLocation } from 'react-router-dom'
import { Search } from './Search'

export function Layout() {
  const { pathname } = useLocation()
  const home = pathname === '/'
  const full = pathname.startsWith('/map')
  return (
    <div className={`app${full ? ' app-full' : ''}`}>
      <a className="skip" href="#main">
        Skip to content
      </a>
      <header className="shell">
        <NavLink to="/" className="brand" aria-label="trackside home">
          <svg viewBox="0 0 14 24" aria-hidden="true" className="brand-mark">
            <rect x="0.5" y="0.5" width="13" height="23" rx="6.5" />
            <circle cx="7" cy="7" r="3.6" className="brand-lit" />
            <circle cx="7" cy="17" r="3.6" className="brand-off" />
          </svg>
          trackside
        </NavLink>
        <nav aria-label="Main">
          <NavLink to="/map">Live map</NavLink>
          <NavLink to="/delay-repay">Delay Repay</NavLink>
        </nav>
        {!home && (
          <div className="shell-search">
            <Search />
          </div>
        )}
      </header>
      <main id="main">
        <Outlet />
      </main>
    </div>
  )
}

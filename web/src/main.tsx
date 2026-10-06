import '@fontsource/barlow/400.css'
import '@fontsource/barlow/500.css'
import '@fontsource/barlow/600.css'
import '@fontsource/barlow-semi-condensed/600.css'
import '@fontsource/barlow-semi-condensed/700.css'
import './styles.css'
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { createBrowserRouter, Link, RouterProvider } from 'react-router-dom'
import { Layout } from './components/Layout'
import { Board } from './pages/Board'
import { DelayRepayPage } from './pages/DelayRepayPage'
import { Home } from './pages/Home'
import { MapPage } from './pages/MapPage'
import { Service } from './pages/Service'

function NotFound() {
  return (
    <div className="page">
      <h1>That page doesn’t exist</h1>
      <p className="lede">
        Search for a station or train above, or go to the <Link to="/">start page</Link>.
      </p>
    </div>
  )
}

const router = createBrowserRouter([
  {
    element: <Layout />,
    children: [
      { path: '/', element: <Home /> },
      { path: '/station/:code', element: <Board /> },
      { path: '/train/:uid/:date', element: <Service /> },
      { path: '/map', element: <MapPage /> },
      { path: '/delay-repay', element: <DelayRepayPage /> },
      { path: '*', element: <NotFound /> },
    ],
  },
])

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <RouterProvider router={router} />
  </StrictMode>,
)

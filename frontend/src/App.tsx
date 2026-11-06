import { Outlet, useLocation } from 'react-router-dom'
import { ToastProvider, ToastViewport } from './hooks/useToast'
import Header from './components/Header'

// Layout root: header + routed page (<Outlet/>) + toast viewport, all under ToastProvider.
export default function App() {
  const location = useLocation()
  return (
    <ToastProvider>
      <Header />
      <main className="md:pl-64">
        <div className="max-w-6xl mx-auto px-5 sm:px-10 pb-24">
          {/* Keyed wrapper re-triggers the rise animation on every navigation (app.js .page.active). */}
          <div key={location.pathname} className="page-enter">
            <Outlet />
          </div>
        </div>
      </main>
      <ToastViewport />
    </ToastProvider>
  )
}

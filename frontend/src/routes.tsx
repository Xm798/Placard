import { createBrowserRouter } from 'react-router-dom'
import App from './App'
import PublishPage from './pages/PublishPage'
import FilesPage from './pages/FilesPage'
import SettingsPage from './pages/SettingsPage'
import DocsPage from './pages/DocsPage'
import NotFoundPage from './pages/NotFoundPage'
import LoginPage from './pages/LoginPage'
import RegisterPage from './pages/RegisterPage'
import AdminPage from './pages/AdminPage'

export const router = createBrowserRouter([
  // Sign-in and registration sit outside the <App/> layout: its header loads
  // /api/me, which has no answer for a visitor who has no session yet.
  { path: '/login', element: <LoginPage /> },
  { path: '/register', element: <RegisterPage /> },
  {
    element: <App />,
    children: [
      { index: true, element: <PublishPage /> },
      { path: 'files', element: <FilesPage /> },
      { path: 'settings', element: <SettingsPage /> },
      { path: 'docs', element: <DocsPage /> },
      { path: 'admin', element: <AdminPage /> },
      { path: '*', element: <NotFoundPage /> },
    ],
  },
])

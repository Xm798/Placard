import { useEffect, useState } from 'react'
import { NavLink } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { api } from '../lib/api'
import { LANGUAGES, setLanguage, normalizeLang, type Lang } from '../i18n'
import { useTheme } from '../hooks/useTheme'
import { useToast } from '../hooks/useToast'
import Avatar from './Avatar'
import Brand from './Brand'

// The app's navigation: a fixed sidebar from md up, and below that the same
// sidebar as a drawer behind the top bar's menu button. Every control renders
// once, so there is a single language picker, theme toggle and sign-out.

interface Me {
  display_name?: string
  authz_id?: string
  is_admin?: boolean
}

const NAV = [
  { to: '/', key: 'nav.publish' },
  { to: '/files', key: 'nav.files' },
  { to: '/settings', key: 'nav.settings' },
  { to: '/docs', key: 'nav.docs' },
]

// The admin entry is an affordance, not the gate: /admin and every /api/admin
// route decide for themselves, so hiding the link only keeps it out of the way
// of the people it would not work for.
const ADMIN_NAV = { to: '/admin', key: 'nav.admin' }

function ThemeIcon({ dark }: { dark: boolean }) {
  return dark ? (
    <svg aria-hidden="true" className="h-[18px] w-[18px]" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth="2.5">
      <circle cx="12" cy="12" r="5" />
      <path d="M12 1v2M12 21v2M4.22 4.22l1.42 1.42M18.36 18.36l1.42 1.42M1 12h2M21 12h2M4.22 19.78l1.42-1.42M18.36 5.64l1.42-1.42" />
    </svg>
  ) : (
    <svg aria-hidden="true" className="h-[18px] w-[18px]" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth="2.5">
      <path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z" />
    </svg>
  )
}

export default function Header() {
  const { dark, toggle } = useTheme()
  const { show } = useToast()
  const { t, i18n } = useTranslation()
  const [me, setMe] = useState<Me>({})
  const [mobileOpen, setMobileOpen] = useState(false)
  const nav = me.is_admin ? [...NAV, ADMIN_NAV] : NAV
  const close = () => setMobileOpen(false)

  // /api/me → user name + avatar. 401 is handled by api().
  useEffect(() => {
    api('/api/me')
      .then((r) => r.json())
      .then((d: Me) => setMe(d))
      .catch(() => {}) // 401 already routed to the login page by api()
  }, [])

  function logout() {
    api('/auth/logout', { method: 'POST' })
      // The static "signed out" page rather than the login page: the latter
      // reads as a fresh login prompt, which on a shared device leaves "you are
      // signed out" ambiguous.
      .then(() => location.assign('/auth/logged-out'))
      .catch(() => show(t('header.logoutFailed')))
  }

  return (
    <>
      <header className="md:hidden surface border-b-[3px] border-line sticky top-0 z-40 h-16 px-4 flex items-center justify-between">
        <Brand onClick={close} />
        <button
          aria-label={mobileOpen ? t('header.closeMenu') : t('header.openMenu')}
          aria-expanded={mobileOpen}
          aria-controls="sidebar"
          onClick={() => setMobileOpen((o) => !o)}
          className="btn-outline w-10 h-10 flex items-center justify-center"
        >
          <svg aria-hidden="true" className="h-[18px] w-[18px]" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth="2.5">
            <path d={mobileOpen ? 'M6 6l12 12M18 6L6 18' : 'M4 6h16M4 12h16M4 18h16'} />
          </svg>
        </button>
      </header>

      <aside
        id="sidebar"
        className={
          'sidebar surface fixed z-30 left-0 bottom-0 top-16 md:top-0 w-full sm:w-72 md:w-64 flex-col ' +
          (mobileOpen ? 'flex' : 'hidden md:flex')
        }
      >
        <div className="hidden md:block px-6 pt-7 pb-8">
          <Brand />
        </div>

        <nav className="flex-1 overflow-y-auto px-4 pt-5 md:pt-0 space-y-2" aria-label={t('header.mobileNav')}>
          {nav.map((n, i) => (
            <NavLink
              key={n.to}
              to={n.to}
              end={n.to === '/'}
              onClick={close}
              className={({ isActive }) => 'side-link flex items-baseline gap-3 px-4 py-3' + (isActive ? ' active' : '')}
            >
              <span className="mono text-xs font-bold opacity-70" aria-hidden="true">
                {String(i + 1).padStart(2, '0')}
              </span>
              <span className="font-display text-lg uppercase">{t(n.key)}</span>
            </NavLink>
          ))}
        </nav>

        <div className="sidebar-foot p-4 space-y-3">
          <div className="flex items-center gap-3 min-w-0">
            <Avatar authzId={me.authz_id} name={me.display_name} className="w-10 h-10 text-base" />
            <span className="font-bold text-sm truncate">{me.display_name || ''}</span>
          </div>
          <div className="flex items-center gap-2">
            <select
              aria-label={t('header.language')}
              value={normalizeLang(i18n.language)}
              onChange={(e) => setLanguage(e.target.value as Lang)}
              className="seg flex-1 min-w-0 h-10 px-2 text-sm font-bold surface border-2 border-line-strong"
            >
              {LANGUAGES.map((l) => (
                <option key={l.code} value={l.code}>
                  {l.label}
                </option>
              ))}
            </select>
            <button
              aria-label={t('header.toggleTheme')}
              aria-pressed={dark}
              onClick={toggle}
              className="btn-outline w-10 h-10 shrink-0 flex items-center justify-center"
            >
              <ThemeIcon dark={dark} />
            </button>
          </div>
          <button onClick={logout} className="btn-outline w-full h-10 text-sm uppercase tracking-wide">
            {t('header.logout')}
          </button>
        </div>
      </aside>
    </>
  )
}

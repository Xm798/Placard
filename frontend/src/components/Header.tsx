import { useEffect, useState, type CSSProperties } from 'react'
import { Link, NavLink } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { api } from '../lib/api'
import { LANGUAGES, setLanguage, normalizeLang, type Lang } from '../i18n'
import { useTheme } from '../hooks/useTheme'
import { useToast } from '../hooks/useToast'
import Avatar from './Avatar'

// Ported 1:1 from internal/web/app.html:168-210 + app.js:62-140.
// Nav active styling replicates paintNav (app.js:78-83).

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

// paintNav (app.js:78-83): active → bg var(--hover), fg var(--fg); else transparent/fg-soft.
function navStyle(isActive: boolean): CSSProperties {
  return {
    background: isActive ? 'var(--hover)' : 'transparent',
    color: isActive ? 'var(--fg)' : 'var(--fg-soft)',
  }
}

export default function Header() {
  const { dark, toggle } = useTheme()
  const { show } = useToast()
  const { t, i18n } = useTranslation()
  const [me, setMe] = useState<Me>({})
  const [mobileOpen, setMobileOpen] = useState(false)
  const nav = me.is_admin ? [...NAV, ADMIN_NAV] : NAV

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
    <header
      className="border-b border-line sticky top-0 z-50"
      style={{
        background: 'color-mix(in srgb, var(--bg) 85%, transparent)',
        backdropFilter: 'blur(12px)',
      }}
    >
      <div className="max-w-5xl mx-auto h-16 px-5 sm:px-8 flex items-center justify-between">
        <div className="flex items-center gap-8">
          <Link to="/" aria-label={t('header.home')} className="flex items-center gap-2.5">
            <div
              className="w-8 h-8 rounded-lg flex items-center justify-center mono text-sm font-medium"
              aria-hidden="true"
              style={{ background: 'var(--ink)', color: 'var(--ink-fg)' }}
            >
              &lt;/&gt;
            </div>
            <span className="font-display font-extrabold text-xl tracking-tight">Placard</span>
          </Link>
          <nav className="hidden sm:flex items-center gap-1">
            {nav.map((n) => (
              <NavLink
                key={n.to}
                to={n.to}
                end={n.to === '/'}
                className="px-3.5 py-1.5 text-sm font-medium rounded-lg"
                style={({ isActive }) => navStyle(isActive)}
              >
                {t(n.key)}
              </NavLink>
            ))}
          </nav>
        </div>
        <div className="flex items-center gap-3">
          {/* F14: hamburger trigger (<640px only; the desktop nav above covers sm and up) */}
          <button
            aria-label={mobileOpen ? t('header.closeMenu') : t('header.openMenu')}
            aria-expanded={mobileOpen}
            aria-controls="mobile-nav"
            onClick={() => setMobileOpen((o) => !o)}
            className="sm:hidden w-9 h-9 rounded-lg flex items-center justify-center hover:bg-[var(--hover)] transition-colors text-soft"
          >
            <svg
              aria-hidden="true"
              className={'h-[18px] w-[18px]' + (mobileOpen ? ' hidden' : '')}
              fill="none"
              viewBox="0 0 24 24"
              stroke="currentColor"
              strokeWidth="2"
            >
              <path d="M4 6h16M4 12h16M4 18h16" />
            </svg>
            <svg
              aria-hidden="true"
              className={'h-[18px] w-[18px]' + (mobileOpen ? '' : ' hidden')}
              fill="none"
              viewBox="0 0 24 24"
              stroke="currentColor"
              strokeWidth="2"
            >
              <path d="M6 6l12 12M18 6L6 18" />
            </svg>
          </button>
          <span className="hidden sm:inline text-sm text-soft">{me.display_name || ''}</span>
          <button
            onClick={logout}
            className="px-3 py-1.5 text-sm font-medium rounded-lg hover:bg-[var(--hover)] transition-colors text-soft"
          >
            {t('header.logout')}
          </button>
          <select
            aria-label={t('header.language')}
            value={normalizeLang(i18n.language)}
            onChange={(e) => setLanguage(e.target.value as Lang)}
            className="h-9 rounded-lg px-2 text-sm bg-transparent border border-line-strong text-soft"
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
            className="w-9 h-9 rounded-lg flex items-center justify-center hover:bg-[var(--hover)] transition-colors text-soft"
          >
            <svg
              aria-hidden="true"
              className={'h-[18px] w-[18px]' + (dark ? '' : ' hidden')}
              fill="none"
              viewBox="0 0 24 24"
              stroke="currentColor"
              strokeWidth="2"
            >
              <circle cx="12" cy="12" r="5" />
              <path d="M12 1v2M12 21v2M4.22 4.22l1.42 1.42M18.36 18.36l1.42 1.42M1 12h2M21 12h2M4.22 19.78l1.42-1.42M18.36 5.64l1.42-1.42" />
            </svg>
            <svg
              aria-hidden="true"
              className={'h-[18px] w-[18px]' + (dark ? ' hidden' : '')}
              fill="none"
              viewBox="0 0 24 24"
              stroke="currentColor"
              strokeWidth="2"
            >
              <path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z" />
            </svg>
          </button>
          <div className="w-px h-5" style={{ background: 'var(--line)' }}></div>
          <div className="flex items-center gap-2">
            <Avatar authzId={me.authz_id} name={me.display_name} />
          </div>
        </div>
      </div>
      {/* F14: the hamburger's panel — hidden on desktop; it is how a <640px
          viewport reaches publish / my files / settings at all. */}
      <nav
        id="mobile-nav"
        className={(mobileOpen ? '' : 'hidden ') + 'sm:hidden border-t border-line px-5 py-2'}
        aria-label={t('header.mobileNav')}
      >
        {nav.map((n) => (
          <NavLink
            key={n.to}
            to={n.to}
            end={n.to === '/'}
            onClick={() => setMobileOpen(false)}
            className="block w-full text-left px-3.5 py-2.5 text-sm font-medium rounded-lg"
            style={({ isActive }) => navStyle(isActive)}
          >
            {t(n.key)}
          </NavLink>
        ))}
      </nav>
    </header>
  )
}

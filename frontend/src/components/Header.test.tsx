import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { ToastProvider } from '../hooks/useToast'
import i18n, { LANG_STORAGE_KEY } from '../i18n'
import zhCN from '../i18n/locales/zh-CN'
import Header from './Header'

// Mock api(): /api/me resolves a display_name; anything else resolves ok.
const apiMock = vi.fn((path: string, _opts?: RequestInit) => {
  if (path === '/api/me') {
    return Promise.resolve({ json: () => Promise.resolve({ display_name: 'Ada' }) } as Response)
  }
  return Promise.resolve({} as Response)
})

vi.mock('../lib/api', () => ({
  api: (path: string, opts?: RequestInit) => apiMock(path, opts),
}))

function renderHeader() {
  return render(
    <MemoryRouter>
      <ToastProvider>
        <Header />
      </ToastProvider>
    </MemoryRouter>,
  )
}

beforeEach(() => {
  localStorage.clear()
  document.documentElement.classList.remove('dark')
  apiMock.mockClear()
  // jsdom has no matchMedia; useTheme falls back to it when no saved theme.
  vi.stubGlobal(
    'matchMedia',
    vi.fn().mockReturnValue({ matches: false } as MediaQueryList),
  )
})

afterEach(async () => {
  localStorage.clear()
  document.documentElement.classList.remove('dark')
  await i18n.changeLanguage('en')
})

describe('Header', () => {
  test('renders the four nav labels (desktop + mobile duplicates)', () => {
    renderHeader()
    for (const label of ['Publish', 'My files', 'Settings', 'Docs']) {
      expect(screen.getAllByText(label).length).toBeGreaterThanOrEqual(1)
    }
  })

  test('hamburger toggles mobile panel aria-expanded false → true', async () => {
    const user = userEvent.setup()
    renderHeader()
    const toggle = screen.getByRole('button', { name: 'Open navigation menu' })
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    await user.click(toggle)
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    expect(toggle).toHaveAttribute('aria-label', 'Close navigation menu')
  })

  test('theme button toggles aria-pressed', async () => {
    const user = userEvent.setup()
    localStorage.setItem('page-theme', 'light')
    renderHeader()
    const theme = screen.getByRole('button', { name: 'Toggle light and dark theme' })
    expect(theme).toHaveAttribute('aria-pressed', 'false')
    await user.click(theme)
    expect(theme).toHaveAttribute('aria-pressed', 'true')
  })

  test('logout button is present', () => {
    renderHeader()
    expect(screen.getByRole('button', { name: 'Sign out' })).toBeInTheDocument()
  })

  test('renders display_name from /api/me', async () => {
    renderHeader()
    await waitFor(() => expect(screen.getByText('Ada')).toBeInTheDocument())
    expect(apiMock).toHaveBeenCalledWith('/api/me', undefined)
  })

  // The admin entry is only an affordance — /admin refuses a non-admin on its
  // own — but showing it to everyone would advertise a page most people cannot
  // open.
  test('the admin entry appears only for an admin', async () => {
    renderHeader()
    await waitFor(() => expect(screen.getByText('Ada')).toBeInTheDocument())
    expect(screen.queryByText('Admin')).not.toBeInTheDocument()

    apiMock.mockImplementationOnce(() =>
      Promise.resolve({
        json: () => Promise.resolve({ display_name: 'Grace', is_admin: true }),
      } as Response),
    )
    renderHeader()
    await waitFor(() => expect(screen.getAllByText('Admin').length).toBeGreaterThanOrEqual(1))
    expect(screen.getAllByRole('link', { name: 'Admin' })[0]).toHaveAttribute('href', '/admin')
  })

  test('the language picker starts on English', () => {
    renderHeader()
    expect(screen.getByRole('combobox', { name: 'Language' })).toHaveValue('en')
    expect(document.documentElement.lang).toBe('en')
  })

  // The switch is what a Chinese-reading colleague on an English-defaulting
  // browser reaches for, so it has to survive the reload too.
  test('picking Chinese retranslates the nav, sets <html lang> and persists', async () => {
    const user = userEvent.setup()
    renderHeader()

    await user.selectOptions(screen.getByRole('combobox', { name: 'Language' }), 'zh-CN')

    await waitFor(() =>
      expect(screen.getAllByText(zhCN.nav.publish).length).toBeGreaterThanOrEqual(1),
    )
    expect(screen.getAllByText(zhCN.nav.files).length).toBeGreaterThanOrEqual(1)
    expect(screen.getByRole('button', { name: zhCN.header.logout })).toBeInTheDocument()
    expect(document.documentElement.lang).toBe('zh-CN')
    expect(localStorage.getItem(LANG_STORAGE_KEY)).toBe('zh-CN')
  })
})

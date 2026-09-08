import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { ToastProvider, ToastViewport } from '../hooks/useToast'
import AdminPage from './AdminPage'

const apiMock = vi.fn()
vi.mock('../lib/api', () => ({
  api: (path: string, opts?: RequestInit) => apiMock(path, opts),
}))

function jsonResp(body: unknown): Response {
  return { json: () => Promise.resolve(body) } as Response
}

function httpError(status: number): Error & { status: number } {
  const e = new Error('http ' + status) as Error & { status: number }
  e.status = status
  return e
}

const ALICE = {
  id: 'alice0000000001',
  username: 'alice',
  email: 'alice@example.com',
  display_name: 'Alice',
  is_admin: true,
  disabled: false,
  has_password: true,
  create_time: '2026-07-01T09:30:00Z',
  last_login_at: '2026-07-02T09:30:00Z',
  last_active_at: '2026-07-02T09:35:00Z',
}

const BOB = {
  id: 'bob00000000001',
  username: 'bob',
  display_name: 'Bob',
  is_admin: false,
  disabled: false,
  has_password: true,
  create_time: '2026-07-01T09:30:00Z',
  last_login_at: null,
  last_active_at: null,
}

const SETTINGS = {
  registration_open: false,
  oidc_auto_provision: true,
  upload_max_file_size: 10 * 1024 * 1024,
}

// The happy path every test starts from: alice is the calling admin, bob is an
// ordinary account.
function mockLoaded() {
  apiMock.mockImplementation((path: string) => {
    if (path.startsWith('/api/admin/users'))
      return Promise.resolve(jsonResp({ users: [ALICE, BOB], total: 2, page: 1, page_size: 20, self: ALICE.id }))
    if (path === '/api/admin/settings') return Promise.resolve(jsonResp(SETTINGS))
    return Promise.resolve(jsonResp({}))
  })
}

function renderPage() {
  return render(
    <MemoryRouter>
      <ToastProvider>
        <AdminPage />
        <ToastViewport />
      </ToastProvider>
    </MemoryRouter>,
  )
}

function rowOf(username: string): HTMLElement {
  return screen.getByText('@' + username).closest('div.px-5') as HTMLElement
}

beforeEach(() => {
  apiMock.mockReset()
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('AdminPage', () => {
  // A non-admin who types the URL sees what an unknown route shows, not an
  // admin page with empty tables.
  test('a 403 from the admin API renders the not-found body', async () => {
    apiMock.mockImplementation(() => Promise.reject(httpError(403)))
    renderPage()
    await waitFor(() => expect(screen.getByText('Page not found')).toBeInTheDocument())
    expect(screen.queryByRole('heading', { name: 'Admin' })).not.toBeInTheDocument()
  })

  // Only a 403 is "this page is not yours". A database failure still shows the
  // page, because a frame that never resolves says nothing at all.
  test('a non-403 load failure still renders the page', async () => {
    apiMock.mockImplementation((path: string) => {
      if (path.startsWith('/api/admin/users')) return Promise.reject(httpError(500))
      return Promise.resolve(jsonResp(SETTINGS))
    })
    renderPage()
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Admin' })).toBeInTheDocument())
    expect(screen.getByText('Could not load the account list')).toBeInTheDocument()
    expect(screen.queryByText('Page not found')).not.toBeInTheDocument()
  })

  test('accounts and settings render', async () => {
    mockLoaded()
    renderPage()

    await waitFor(() => expect(screen.getByText('@alice')).toBeInTheDocument())
    expect(screen.getByText('@bob')).toBeInTheDocument()
    expect(screen.getByText('2 accounts in total')).toBeInTheDocument()
    expect(screen.getByText('Administrator')).toBeInTheDocument()
    expect(within(rowOf('bob')).getByText('never signed in')).toBeInTheDocument()

    expect(screen.getByRole('checkbox', { name: /Open registration/ })).not.toBeChecked()
    expect(screen.getByRole('checkbox', { name: /Single sign-on auto-provisioning/ })).toBeChecked()
    expect(screen.getByLabelText(/Upload size limit/)).toHaveValue(10)
  })

  // The server refuses these on the caller's own account; the page says so
  // before the request rather than after it.
  // English needs the singular; the count is interpolated, so "1 accounts" is
  // the shape this gets wrong.
  test('a single account reads in the singular', async () => {
    apiMock.mockImplementation((path: string) => {
      if (path.startsWith('/api/admin/users'))
        return Promise.resolve(jsonResp({ users: [ALICE], total: 1, page: 1, page_size: 20, self: ALICE.id }))
      return Promise.resolve(jsonResp(SETTINGS))
    })
    renderPage()
    await waitFor(() => expect(screen.getByText('1 account in total')).toBeInTheDocument())
  })

  test("the calling admin's own destructive controls are disabled", async () => {
    mockLoaded()
    renderPage()
    await waitFor(() => expect(screen.getByText('@alice')).toBeInTheDocument())

    const alice = within(rowOf('alice'))
    expect(alice.getByRole('button', { name: 'Disable' })).toBeDisabled()
    expect(alice.getByRole('button', { name: 'Remove admin' })).toBeDisabled()
    expect(alice.getByRole('button', { name: 'Delete' })).toBeDisabled()
    // Resetting one's own password locks nobody out, so it stays available.
    expect(alice.getByRole('button', { name: 'Reset password' })).toBeEnabled()

    const bob = within(rowOf('bob'))
    expect(bob.getByRole('button', { name: 'Disable' })).toBeEnabled()
    expect(bob.getByRole('button', { name: 'Delete' })).toBeEnabled()
  })

  test('disabling confirms, PATCHes disabled:true and reloads', async () => {
    const user = userEvent.setup()
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    mockLoaded()
    renderPage()
    await waitFor(() => expect(screen.getByText('@bob')).toBeInTheDocument())

    await user.click(within(rowOf('bob')).getByRole('button', { name: 'Disable' }))

    await waitFor(() =>
      expect(apiMock).toHaveBeenCalledWith(
        '/api/admin/users/' + BOB.id,
        expect.objectContaining({ method: 'PATCH', body: JSON.stringify({ disabled: true }) }),
      ),
    )
  })

  test('promoting PATCHes is_admin:true without a confirmation', async () => {
    const user = userEvent.setup()
    const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(true)
    mockLoaded()
    renderPage()
    await waitFor(() => expect(screen.getByText('@bob')).toBeInTheDocument())

    await user.click(within(rowOf('bob')).getByRole('button', { name: 'Make admin' }))

    await waitFor(() =>
      expect(apiMock).toHaveBeenCalledWith(
        '/api/admin/users/' + BOB.id,
        expect.objectContaining({ method: 'PATCH', body: JSON.stringify({ is_admin: true }) }),
      ),
    )
    expect(confirmSpy).not.toHaveBeenCalled()
  })

  test('deleting confirms and DELETEs', async () => {
    const user = userEvent.setup()
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    mockLoaded()
    renderPage()
    await waitFor(() => expect(screen.getByText('@bob')).toBeInTheDocument())

    await user.click(within(rowOf('bob')).getByRole('button', { name: 'Delete' }))

    await waitFor(() =>
      expect(apiMock).toHaveBeenCalledWith(
        '/api/admin/users/' + BOB.id,
        expect.objectContaining({ method: 'DELETE' }),
      ),
    )
  })

  test('a cancelled confirmation sends nothing', async () => {
    const user = userEvent.setup()
    vi.spyOn(window, 'confirm').mockReturnValue(false)
    mockLoaded()
    renderPage()
    await waitFor(() => expect(screen.getByText('@bob')).toBeInTheDocument())
    apiMock.mockClear()

    await user.click(within(rowOf('bob')).getByRole('button', { name: 'Delete' }))
    expect(apiMock).not.toHaveBeenCalled()
  })

  // One account action at a time: while a request is in flight every row says
  // so, rather than letting a confirmed click be swallowed.
  test('an in-flight action disables the other rows', async () => {
    const user = userEvent.setup()
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    apiMock.mockImplementation((path: string, opts?: RequestInit) => {
      if (opts?.method === 'DELETE') return new Promise<Response>(() => {})
      if (path.startsWith('/api/admin/users'))
        return Promise.resolve(jsonResp({ users: [ALICE, BOB], total: 2, page: 1, page_size: 20, self: ALICE.id }))
      return Promise.resolve(jsonResp(SETTINGS))
    })
    renderPage()
    await waitFor(() => expect(screen.getByText('@bob')).toBeInTheDocument())

    await user.click(within(rowOf('bob')).getByRole('button', { name: 'Delete' }))

    await waitFor(() => expect(within(rowOf('bob')).getByRole('button', { name: 'Reset password' })).toBeDisabled())
    expect(within(rowOf('alice')).getByRole('button', { name: 'Reset password' })).toBeDisabled()
  })

  // A 400 from the password endpoint is the server refusing the password, not
  // the self-lockout guard the flag endpoints answer 400 for.
  test('a password the server refuses reports what actually happened', async () => {
    const user = userEvent.setup()
    vi.spyOn(window, 'prompt').mockReturnValue('a'.repeat(50))
    apiMock.mockImplementation((path: string) => {
      if (path.endsWith('/password')) return Promise.reject(httpError(400))
      if (path.startsWith('/api/admin/users'))
        return Promise.resolve(jsonResp({ users: [ALICE, BOB], total: 2, page: 1, page_size: 20, self: ALICE.id }))
      return Promise.resolve(jsonResp(SETTINGS))
    })
    renderPage()
    await waitFor(() => expect(screen.getByText('@bob')).toBeInTheDocument())

    await user.click(within(rowOf('bob')).getByRole('button', { name: 'Reset password' }))

    await waitFor(() => expect(screen.getByText('The server refused that password — pick another one')).toBeInTheDocument())
  })

  test('resetting a password posts what the prompt returned', async () => {
    const user = userEvent.setup()
    vi.spyOn(window, 'prompt').mockReturnValue('battery-staple')
    mockLoaded()
    renderPage()
    await waitFor(() => expect(screen.getByText('@bob')).toBeInTheDocument())

    await user.click(within(rowOf('bob')).getByRole('button', { name: 'Reset password' }))

    await waitFor(() =>
      expect(apiMock).toHaveBeenCalledWith(
        '/api/admin/users/' + BOB.id + '/password',
        expect.objectContaining({ method: 'POST', body: JSON.stringify({ password: 'battery-staple' }) }),
      ),
    )
  })

  // The server enforces the length too; refusing here keeps a request that
  // cannot succeed off the wire.
  test('a too-short password is refused before the request', async () => {
    const user = userEvent.setup()
    vi.spyOn(window, 'prompt').mockReturnValue('short')
    mockLoaded()
    renderPage()
    await waitFor(() => expect(screen.getByText('@bob')).toBeInTheDocument())
    apiMock.mockClear()

    await user.click(within(rowOf('bob')).getByRole('button', { name: 'Reset password' }))
    await waitFor(() => expect(screen.getByText('A password needs at least 8 characters')).toBeInTheDocument())
    expect(apiMock).not.toHaveBeenCalled()
  })

  test('toggling a setting PATCHes it and adopts the response', async () => {
    const user = userEvent.setup()
    apiMock.mockImplementation((path: string, opts?: RequestInit) => {
      if (path.startsWith('/api/admin/users'))
        return Promise.resolve(jsonResp({ users: [ALICE], total: 1, page: 1, page_size: 20, self: ALICE.id }))
      if (path === '/api/admin/settings' && opts?.method === 'PATCH')
        return Promise.resolve(jsonResp({ ...SETTINGS, registration_open: true }))
      return Promise.resolve(jsonResp(SETTINGS))
    })
    renderPage()
    await waitFor(() => expect(screen.getByRole('checkbox', { name: /Open registration/ })).not.toBeChecked())

    await user.click(screen.getByRole('checkbox', { name: /Open registration/ }))

    await waitFor(() =>
      expect(apiMock).toHaveBeenCalledWith(
        '/api/admin/settings',
        expect.objectContaining({ method: 'PATCH', body: JSON.stringify({ registration_open: true }) }),
      ),
    )
    await waitFor(() => expect(screen.getByRole('checkbox', { name: /Open registration/ })).toBeChecked())
  })

  test('the upload limit is sent in bytes', async () => {
    const user = userEvent.setup()
    mockLoaded()
    renderPage()
    await waitFor(() => expect(screen.getByLabelText(/Upload size limit/)).toHaveValue(10))

    const input = screen.getByLabelText(/Upload size limit/)
    await user.clear(input)
    await user.type(input, '25')
    await user.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() =>
      expect(apiMock).toHaveBeenCalledWith(
        '/api/admin/settings',
        expect.objectContaining({
          method: 'PATCH',
          body: JSON.stringify({ upload_max_file_size: 25 * 1024 * 1024 }),
        }),
      ),
    )
  })

  test('a non-positive upload limit is refused before the request', async () => {
    const user = userEvent.setup()
    mockLoaded()
    renderPage()
    await waitFor(() => expect(screen.getByLabelText(/Upload size limit/)).toHaveValue(10))
    apiMock.mockClear()

    await user.clear(screen.getByLabelText(/Upload size limit/))
    await user.type(screen.getByLabelText(/Upload size limit/), '0')
    await user.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() => expect(screen.getByText('The upload limit must be a positive number')).toBeInTheDocument())
    expect(apiMock).not.toHaveBeenCalled()
  })
})

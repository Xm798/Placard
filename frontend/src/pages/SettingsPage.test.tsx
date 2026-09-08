import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { ToastProvider } from '../hooks/useToast'
import SettingsPage from './SettingsPage'

// Mock api(): dispatch by path + method. Each test overrides the implementation.
const apiMock = vi.fn()
vi.mock('../lib/api', () => ({
  api: (path: string, opts?: RequestInit) => apiMock(path, opts),
}))

function jsonResp(body: unknown): Response {
  return { json: () => Promise.resolve(body) } as Response
}

function renderPage() {
  return render(
    <ToastProvider>
      <SettingsPage />
    </ToastProvider>,
  )
}

beforeEach(() => {
  apiMock.mockReset()
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('SettingsPage', () => {
  test('empty tokens → empty copy shown', async () => {
    apiMock.mockImplementation((path: string) => {
      if (path === '/api/tokens') return Promise.resolve(jsonResp({ tokens: [] }))
      return Promise.resolve(jsonResp({}))
    })
    renderPage()
    await waitFor(() =>
      expect(screen.getByText('No tokens yet — use “Create API Token” above.')).toBeInTheDocument(),
    )
  })

  test('one token renders name + meta string (created / long-lived / never used)', async () => {
    apiMock.mockImplementation((path: string) => {
      if (path === '/api/tokens')
        return Promise.resolve(
          jsonResp({
            tokens: [
              {
                id: 1,
                name: 'My laptop',
                create_time: '2026-07-01T09:30:00Z',
                last_used_at: null,
                expires_at: null,
                revoked: false,
              },
            ],
          }),
        )
      return Promise.resolve(jsonResp({}))
    })
    renderPage()
    await waitFor(() => expect(screen.getByText('My laptop')).toBeInTheDocument())
    const meta = screen.getByText(/created/)
    expect(meta.textContent).toContain('created')
    expect(meta.textContent).toContain('long-lived')
    expect(meta.textContent).toContain('never used')
  })

  test('revoke: confirm→true, DELETE ok, api called /api/tokens/1 DELETE', async () => {
    const user = userEvent.setup()
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    apiMock.mockImplementation((path: string, opts?: RequestInit) => {
      if (path === '/api/tokens' && (!opts || !opts.method))
        return Promise.resolve(
          jsonResp({
            tokens: [
              {
                id: 1,
                name: 'My laptop',
                create_time: '2026-07-01T09:30:00Z',
                last_used_at: null,
                expires_at: null,
                revoked: false,
              },
            ],
          }),
        )
      return Promise.resolve(jsonResp({}))
    })
    renderPage()
    await waitFor(() => expect(screen.getByText('My laptop')).toBeInTheDocument())

    await user.click(screen.getByRole('button', { name: 'Revoke: My laptop' }))

    await waitFor(() =>
      expect(apiMock).toHaveBeenCalledWith('/api/tokens/1', { method: 'DELETE' }),
    )
  })

  test('revoked token renders delete and can be permanently deleted', async () => {
    const user = userEvent.setup()
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    apiMock.mockImplementation((path: string, opts?: RequestInit) => {
      if (path === '/api/tokens' && (!opts || !opts.method))
        return Promise.resolve(
          jsonResp({
            tokens: [
              {
                id: 2,
                name: 'Old token',
                create_time: '2026-07-01T09:30:00Z',
                last_used_at: null,
                expires_at: null,
                revoked: true,
              },
            ],
          }),
        )
      return Promise.resolve(jsonResp({}))
    })
    renderPage()

    await user.click(await screen.findByRole('button', { name: 'Delete: Old token' }))

    expect(screen.queryByRole('button', { name: 'Revoke: Old token' })).not.toBeInTheDocument()
    await waitFor(() =>
      expect(apiMock).toHaveBeenCalledWith('/api/tokens/2/permanent', { method: 'DELETE' }),
    )
  })

  test('create modal: type name, POST returns token → plaintext modal shows it', async () => {
    const user = userEvent.setup()
    apiMock.mockImplementation((path: string, opts?: RequestInit) => {
      if (path === '/api/tokens' && opts?.method === 'POST')
        return Promise.resolve(jsonResp({ token: 'pl_secret' }))
      if (path === '/api/tokens') return Promise.resolve(jsonResp({ tokens: [] }))
      return Promise.resolve(jsonResp({}))
    })
    renderPage()
    await waitFor(() =>
      expect(screen.getByText('No tokens yet — use “Create API Token” above.')).toBeInTheDocument(),
    )

    await user.click(screen.getByRole('button', { name: 'Create API Token' }))

    const nameInput = await screen.findByLabelText('Name')
    await user.type(nameInput, 'CI pipeline')
    await user.click(screen.getByRole('button', { name: 'Generate' }))

    // POST body carries trimmed name + default 90d expiry.
    await waitFor(() =>
      expect(apiMock).toHaveBeenCalledWith(
        '/api/tokens',
        expect.objectContaining({
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ name: 'CI pipeline', expiry: '90d' }),
        }),
      ),
    )

    // Plaintext modal appears with the secret + export line.
    const dialog = await screen.findByRole('dialog', { name: 'Token created' })
    expect(within(dialog).getByText('pl_secret')).toBeInTheDocument()
    expect(within(dialog).getByText('export PLACARD_TOKEN=pl_secret')).toBeInTheDocument()
  })

  test('default-visibility card: GET /api/prefs seeds the selection', async () => {
    apiMock.mockImplementation((path: string) => {
      if (path === '/api/prefs') return Promise.resolve(jsonResp({ default_visibility: 'private' }))
      if (path === '/api/tokens') return Promise.resolve(jsonResp({ tokens: [] }))
      return Promise.resolve(jsonResp({}))
    })
    renderPage()
    await waitFor(() =>
      expect(screen.getByRole('radio', { name: 'Private' })).toHaveAttribute('aria-checked', 'true'),
    )
    expect(screen.getByRole('radio', { name: 'Link' })).toHaveAttribute('aria-checked', 'false')
  })

  test('default-visibility card: switching tier PUTs /api/prefs', async () => {
    const user = userEvent.setup()
    apiMock.mockImplementation((path: string, opts?: RequestInit) => {
      if (path === '/api/prefs' && opts?.method === 'PUT') return Promise.resolve(jsonResp({}))
      if (path === '/api/prefs') return Promise.resolve(jsonResp({ default_visibility: 'link' }))
      if (path === '/api/tokens') return Promise.resolve(jsonResp({ tokens: [] }))
      return Promise.resolve(jsonResp({}))
    })
    renderPage()
    await waitFor(() =>
      expect(screen.getByRole('radio', { name: 'Link' })).toHaveAttribute('aria-checked', 'true'),
    )

    await user.click(screen.getByRole('radio', { name: 'Private' }))

    await waitFor(() =>
      expect(apiMock).toHaveBeenCalledWith(
        '/api/prefs',
        expect.objectContaining({
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ default_visibility: 'private' }),
        }),
      ),
    )
  })

  test('default-visibility card: a 503 PUT rolls the selection back', async () => {
    const user = userEvent.setup()
    apiMock.mockImplementation((path: string, opts?: RequestInit) => {
      if (path === '/api/prefs' && opts?.method === 'PUT') {
        const e = new Error('http 503') as Error & { status?: number }
        e.status = 503
        return Promise.reject(e)
      }
      if (path === '/api/prefs') return Promise.resolve(jsonResp({ default_visibility: 'link' }))
      if (path === '/api/tokens') return Promise.resolve(jsonResp({ tokens: [] }))
      return Promise.resolve(jsonResp({}))
    })
    renderPage()
    await waitFor(() =>
      expect(screen.getByRole('radio', { name: 'Link' })).toHaveAttribute('aria-checked', 'true'),
    )

    await user.click(screen.getByRole('radio', { name: 'Private' }))

    // Rejected PUT rolls the optimistic switch back to the server value.
    await waitFor(() =>
      expect(screen.getByRole('radio', { name: 'Link' })).toHaveAttribute('aria-checked', 'true'),
    )
  })
})

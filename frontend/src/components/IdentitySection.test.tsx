import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { ToastProvider } from '../hooks/useToast'
import IdentitySection from './IdentitySection'

const apiMock = vi.fn()
vi.mock('../lib/api', () => ({
  api: (path: string, opts?: RequestInit) => apiMock(path, opts),
}))

function jsonResp(body: unknown): Response {
  return { json: () => Promise.resolve(body) } as Response
}

function renderSection() {
  return render(
    <ToastProvider>
      <IdentitySection />
    </ToastProvider>,
  )
}

beforeEach(() => {
  apiMock.mockReset()
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('IdentitySection', () => {
  test('linked providers are listed and unlinkable ones show an enabled button', async () => {
    apiMock.mockResolvedValue(
      jsonResp({
        identities: [
          { provider: 'corp', display_name: 'Company SSO', linked_at: '2026-07-01T09:30:00Z', can_unlink: true },
        ],
        has_password: true,
        available: [{ name: 'lab', display_name: 'Lab IdP' }],
      }),
    )
    renderSection()

    await waitFor(() => expect(screen.getByText('Company SSO')).toBeInTheDocument())
    expect(screen.getByRole('button', { name: 'Unlink' })).toBeEnabled()
    // An unbound provider is an invitation to link, which is a navigation.
    expect(screen.getByRole('link', { name: /Lab IdP/ })).toHaveAttribute(
      'href',
      '/auth/oidc/start?provider=lab&redirect=%2Fsettings',
    )
  })

  // The server refuses to remove the last credential; the button says so
  // before the request rather than after it.
  test('the only login method cannot be unlinked', async () => {
    apiMock.mockResolvedValue(
      jsonResp({
        identities: [
          { provider: 'corp', display_name: 'Company SSO', linked_at: '2026-07-01T09:30:00Z', can_unlink: false },
        ],
        has_password: false,
      }),
    )
    renderSection()

    await waitFor(() => expect(screen.getByRole('button', { name: 'Unlink' })).toBeDisabled())
    expect(screen.getByText(/no password/)).toBeInTheDocument()
  })

  test('unlinking confirms, calls DELETE and reloads the list', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    apiMock.mockImplementation((_path: string, opts?: RequestInit) => {
      if (opts?.method === 'DELETE') return Promise.resolve(jsonResp({}))
      return Promise.resolve(
        jsonResp({
          identities: [
            { provider: 'corp', display_name: 'Company SSO', linked_at: '2026-07-01T09:30:00Z', can_unlink: true },
          ],
          has_password: true,
        }),
      )
    })
    renderSection()

    await waitFor(() => expect(screen.getByRole('button', { name: 'Unlink' })).toBeInTheDocument())
    await userEvent.click(screen.getByRole('button', { name: 'Unlink' }))

    await waitFor(() =>
      expect(apiMock).toHaveBeenCalledWith('/api/auth/identities/corp', { method: 'DELETE' }),
    )
  })

  // A password-only instance with nothing linked has nothing to manage.
  test('renders nothing when no provider is configured or linked', async () => {
    apiMock.mockResolvedValue(jsonResp({ identities: [], has_password: true }))
    const { container } = renderSection()

    await waitFor(() => expect(apiMock).toHaveBeenCalledWith('/api/auth/identities', undefined))
    expect(container.textContent).not.toContain('Sign-in methods')
  })
})

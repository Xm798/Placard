import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import LoginPage from './LoginPage'

const fetchAuthStatus = vi.fn()
const login = vi.fn()
const navigate = vi.fn()

vi.mock('../lib/auth', async () => {
  const actual = await vi.importActual<typeof import('../lib/auth')>('../lib/auth')
  return {
    ...actual,
    fetchAuthStatus: () => fetchAuthStatus(),
    login: (identifier: string, password: string) => login(identifier, password),
  }
})

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual<typeof import('react-router-dom')>('react-router-dom')
  return { ...actual, useNavigate: () => navigate }
})

function renderPage(search = '') {
  return render(
    <MemoryRouter initialEntries={['/login' + search]}>
      <LoginPage />
    </MemoryRouter>,
  )
}

beforeEach(() => {
  fetchAuthStatus.mockReset()
  login.mockReset()
  navigate.mockReset()
  fetchAuthStatus.mockResolvedValue({ registration_open: true, setup_required: false })
  vi.stubGlobal('location', {
    assign: vi.fn(),
    pathname: '/login',
    origin: 'https://placard.example',
  })
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('LoginPage', () => {
  test('signing in posts the credentials and lands on the redirect target', async () => {
    login.mockResolvedValue({ id: 'u1', username: 'alice', display_name: 'Alice', is_admin: false })
    renderPage('?redirect=%2Ffiles')

    await userEvent.type(screen.getByLabelText('Username or email'), 'alice')
    await userEvent.type(screen.getByLabelText('Password'), 'correct-horse')
    await userEvent.click(screen.getByRole('button', { name: 'Sign in' }))

    await waitFor(() => expect(login).toHaveBeenCalledWith('alice', 'correct-horse'))
    await waitFor(() => expect(location.assign).toHaveBeenCalledWith('/files'))
  })

  // Anything that resolves off this origin would turn the login page into an
  // open redirect. The backslash forms matter as much as the obvious ones: the
  // URL parser reads "\\" as "/" for http(s).
  test.each([
    ['absolute', '?redirect=https%3A%2F%2Fevil.example%2Fx'],
    ['protocol-relative', '?redirect=%2F%2Fevil.example'],
    ['backslash', '?redirect=%2F%5Cevil.example'],
    ['double backslash', '?redirect=%5C%5Cevil.example'],
  ])('an off-site redirect target (%s) falls back to the app root', async (_name, search) => {
    login.mockResolvedValue({ id: 'u1', username: 'alice', display_name: 'Alice', is_admin: false })
    renderPage(search)

    await userEvent.type(screen.getByLabelText('Username or email'), 'alice')
    await userEvent.type(screen.getByLabelText('Password'), 'correct-horse')
    await userEvent.click(screen.getByRole('button', { name: 'Sign in' }))

    await waitFor(() => expect(location.assign).toHaveBeenCalledWith('/'))
  })

  test('a rejected sign-in shows the server message and stays on the page', async () => {
    login.mockRejectedValue(new Error('invalid credentials'))
    renderPage()

    await userEvent.type(screen.getByLabelText('Username or email'), 'alice')
    await userEvent.type(screen.getByLabelText('Password'), 'nope-nope')
    await userEvent.click(screen.getByRole('button', { name: 'Sign in' }))

    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('invalid credentials'))
    expect(location.assign).not.toHaveBeenCalled()
  })

  // "Registration is closed" is a claim about policy: a status that never
  // arrived must not be reported as one.
  test('a failed status probe does not claim registration is closed', async () => {
    fetchAuthStatus.mockRejectedValue(new Error('boom'))
    renderPage()

    await waitFor(() => expect(screen.getByText(/Could not load sign-in options/)).toBeInTheDocument())
    expect(screen.queryByText(/Registration is closed/)).not.toBeInTheDocument()
  })

  test('closed registration hides the create-account link', async () => {
    fetchAuthStatus.mockResolvedValue({ registration_open: false, setup_required: false })
    renderPage()

    await waitFor(() => expect(screen.getByText(/Registration is closed/)).toBeInTheDocument())
    expect(screen.queryByRole('link', { name: 'Create one' })).not.toBeInTheDocument()
  })

  // One button per configured provider, each carrying the post-login
  // destination so an SSO sign-in lands where a password sign-in would.
  test('configured providers each get a sign-in button', async () => {
    fetchAuthStatus.mockResolvedValue({
      registration_open: false,
      setup_required: false,
      oidc_providers: [
        { name: 'corp', display_name: 'Company SSO' },
        { name: 'lab', display_name: 'Lab IdP' },
      ],
    })
    renderPage('?redirect=%2Ffiles')

    const corp = await screen.findByRole('link', { name: 'Continue with Company SSO' })
    expect(corp).toHaveAttribute('href', '/auth/oidc/start?provider=corp&redirect=%2Ffiles')
    expect(screen.getByRole('link', { name: 'Continue with Lab IdP' })).toHaveAttribute(
      'href',
      '/auth/oidc/start?provider=lab&redirect=%2Ffiles',
    )
  })

  test('a password-only instance renders no provider buttons', async () => {
    fetchAuthStatus.mockResolvedValue({ registration_open: true, setup_required: false })
    renderPage()

    await waitFor(() => expect(screen.getByRole('button', { name: 'Sign in' })).toBeInTheDocument())
    expect(screen.queryByRole('link', { name: /Continue with/ })).not.toBeInTheDocument()
  })

  // An instance with no accounts has nothing to sign in to yet.
  test('an empty instance is sent to registration', async () => {
    fetchAuthStatus.mockResolvedValue({ registration_open: true, setup_required: true })
    renderPage()

    await waitFor(() => expect(navigate).toHaveBeenCalledWith('/register', { replace: true }))
  })
})

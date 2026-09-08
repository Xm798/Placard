import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import RegisterPage from './RegisterPage'

const fetchAuthStatus = vi.fn()
const register = vi.fn()
const navigate = vi.fn()

vi.mock('../lib/auth', async () => {
  const actual = await vi.importActual<typeof import('../lib/auth')>('../lib/auth')
  return {
    ...actual,
    fetchAuthStatus: () => fetchAuthStatus(),
    register: (input: unknown) => register(input),
  }
})

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual<typeof import('react-router-dom')>('react-router-dom')
  return { ...actual, useNavigate: () => navigate }
})

function renderPage() {
  return render(
    <MemoryRouter initialEntries={['/register']}>
      <RegisterPage />
    </MemoryRouter>,
  )
}

beforeEach(() => {
  fetchAuthStatus.mockReset()
  register.mockReset()
  navigate.mockReset()
  vi.stubGlobal('location', {
    assign: vi.fn(),
    pathname: '/register',
    origin: 'https://placard.example',
  })
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('RegisterPage', () => {
  test('the first account is announced as the admin account', async () => {
    fetchAuthStatus.mockResolvedValue({ registration_open: true, setup_required: true })
    renderPage()

    await waitFor(() => expect(screen.getByText('Create the admin account')).toBeInTheDocument())
    expect(screen.getByRole('button', { name: 'Create admin account' })).toBeInTheDocument()
  })

  test('submitting sends the whole form and lands in the app', async () => {
    fetchAuthStatus.mockResolvedValue({ registration_open: true, setup_required: true })
    register.mockResolvedValue({ id: 'u1', username: 'alice', display_name: 'Alice', is_admin: true })
    renderPage()

    await userEvent.type(screen.getByLabelText('Username'), 'alice')
    await userEvent.type(screen.getByLabelText('Display name'), 'Alice')
    await userEvent.type(screen.getByLabelText('Email'), 'alice@example.com')
    await userEvent.type(screen.getByLabelText('Password'), 'correct-horse')
    await userEvent.click(screen.getByRole('button', { name: 'Create admin account' }))

    await waitFor(() =>
      expect(register).toHaveBeenCalledWith({
        username: 'alice',
        display_name: 'Alice',
        email: 'alice@example.com',
        password: 'correct-horse',
      }),
    )
    await waitFor(() => expect(location.assign).toHaveBeenCalledWith('/'))
  })

  test('a rejected registration shows the server message', async () => {
    fetchAuthStatus.mockResolvedValue({ registration_open: true, setup_required: false })
    register.mockRejectedValue(new Error('username or email already in use'))
    renderPage()

    await userEvent.type(screen.getByLabelText('Username'), 'alice')
    await userEvent.type(screen.getByLabelText('Password'), 'correct-horse')
    await userEvent.click(screen.getByRole('button', { name: 'Create account' }))

    await waitFor(() =>
      expect(screen.getByRole('alert')).toHaveTextContent('username or email already in use'),
    )
  })

  // With registration closed there is no form to fill in, only the way back.
  test('closed registration redirects to sign-in', async () => {
    fetchAuthStatus.mockResolvedValue({ registration_open: false, setup_required: false })
    renderPage()

    await waitFor(() => expect(navigate).toHaveBeenCalledWith('/login', { replace: true }))
  })
})

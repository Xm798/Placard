import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, test, vi } from 'vitest'
import { ToastProvider, ToastViewport } from '../hooks/useToast'
import AccountSection from './AccountSection'

const apiMock = vi.fn()
vi.mock('../lib/api', () => ({
  api: (path: string, opts?: RequestInit) => apiMock(path, opts),
}))

function jsonResp(body: unknown): Response {
  return { json: () => Promise.resolve(body) } as Response
}

function httpError(status: number) {
  return Object.assign(new Error('http ' + status), { status })
}

function renderSection(profile: object, onPut: () => Promise<unknown> = () => Promise.resolve(jsonResp({}))) {
  apiMock.mockImplementation((path: string, opts?: RequestInit) => {
    if (path === '/api/account') return Promise.resolve(jsonResp(profile))
    if (path === '/api/account/password' && opts?.method === 'PUT') return onPut()
    return Promise.reject(httpError(404))
  })
  return render(
    <ToastProvider>
      <AccountSection />
      <ToastViewport />
    </ToastProvider>,
  )
}

beforeEach(() => {
  apiMock.mockReset()
})

describe('AccountSection', () => {
  test('shows the username and email', async () => {
    renderSection({ username: 'alice', email: 'alice@example.com', has_password: true })
    expect(await screen.findByText('alice@example.com')).toBeInTheDocument()
    expect(screen.getByText('alice')).toBeInTheDocument()
  })

  test('an account without an email says so', async () => {
    renderSection({ username: 'alice', has_password: true })
    expect(await screen.findByText('Not set')).toBeInTheDocument()
  })

  test('sends the current and new password', async () => {
    const user = userEvent.setup()
    renderSection({ username: 'alice', has_password: true })
    await user.type(await screen.findByLabelText('Current password'), 'correct-horse')
    await user.type(screen.getByLabelText('New password'), 'battery-staple')
    await user.type(screen.getByLabelText('Confirm new password'), 'battery-staple')
    await user.click(screen.getByRole('button', { name: 'Update password' }))

    await waitFor(() => expect(screen.getByText('Password updated. Other devices have been signed out.')).toBeInTheDocument())
    const put = apiMock.mock.calls.find(([path]) => path === '/api/account/password')
    expect(JSON.parse(put![1].body)).toEqual({ current_password: 'correct-horse', new_password: 'battery-staple' })
    expect(screen.getByLabelText('New password')).toHaveValue('')
  })

  test('mismatched confirmation is caught before any request', async () => {
    const user = userEvent.setup()
    renderSection({ username: 'alice', has_password: true })
    await user.type(await screen.findByLabelText('Current password'), 'correct-horse')
    await user.type(screen.getByLabelText('New password'), 'battery-staple')
    await user.type(screen.getByLabelText('Confirm new password'), 'battery-stapler')
    await user.click(screen.getByRole('button', { name: 'Update password' }))

    expect(screen.getByRole('alert')).toHaveTextContent('The two new passwords do not match')
    expect(apiMock.mock.calls.some(([path]) => path === '/api/account/password')).toBe(false)
  })

  test('a wrong current password is reported inline', async () => {
    const user = userEvent.setup()
    renderSection({ username: 'alice', has_password: true }, () => Promise.reject(httpError(403)))
    await user.type(await screen.findByLabelText('Current password'), 'wrong-horse')
    await user.type(screen.getByLabelText('New password'), 'battery-staple')
    await user.type(screen.getByLabelText('Confirm new password'), 'battery-staple')
    await user.click(screen.getByRole('button', { name: 'Update password' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('The current password is incorrect')
  })

  test('an account with no password sets one without the current field', async () => {
    renderSection({ username: 'alice', has_password: false })
    expect(await screen.findByText('Set a password')).toBeInTheDocument()
    expect(screen.queryByLabelText('Current password')).toBeNull()
  })
})

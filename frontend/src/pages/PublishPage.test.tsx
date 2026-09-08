import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { ToastProvider } from '../hooks/useToast'
import i18n from '../i18n'
import { fmtDate } from '../lib/format'
import type { HttpError } from '../lib/api'
import PublishPage, { publishErrMsg } from './PublishPage'

const t = i18n.t.bind(i18n)

// Mock api(): each test overrides its resolved/rejected value.
const apiMock = vi.fn()
vi.mock('../lib/api', () => ({
  api: (path: string, opts?: RequestInit) => apiMock(path, opts),
}))

function renderPage(initialEntries: string[] = ['/']) {
  return render(
    <MemoryRouter initialEntries={initialEntries}>
      <ToastProvider>
        <PublishPage />
      </ToastProvider>
    </MemoryRouter>,
  )
}

// Build a File whose size (file.size) reports `bytes` without allocating them.
function makeFile(name: string, type: string, bytes: number): File {
  const f = new File(['x'], name, { type })
  Object.defineProperty(f, 'size', { value: bytes })
  return f
}

// Drop a file onto the hidden file input (the dropzone wraps <input type=file>).
async function selectFile(file: File) {
  const input = document.getElementById('file-input') as HTMLInputElement
  Object.defineProperty(input, 'files', { value: [file], configurable: true })
  input.dispatchEvent(new Event('change', { bubbles: true }))
}

// findCall: locate a specific call among apiMock's invocations.
function findCall(path: string) {
  const call = apiMock.mock.calls.find((c) => c[0] === path)
  if (!call) throw new Error('no api() call for ' + path)
  return call
}

beforeEach(() => {
  apiMock.mockReset()
  // Default: every call resolves to an empty body unless a test overrides it.
  // Individual tests still call mockResolvedValue()/mockImplementation() for
  // the publish-specific shape.
  apiMock.mockResolvedValue({ json: () => Promise.resolve({}) } as Response)
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('PublishPage', () => {
  test('a >10MB .html file shows the 10MB error card', async () => {
    renderPage()
    await selectFile(makeFile('big.html', 'text/html', 11 * 1024 * 1024))
    await waitFor(() =>
      expect(screen.getByText('The file is over the 10MB limit. Compress it and try again.')).toBeInTheDocument(),
    )
    expect(screen.getByText('Publishing failed')).toBeInTheDocument()
    expect(apiMock).not.toHaveBeenCalledWith('/api/publish', expect.anything())
  })

  test('a .png file shows the type error copy', async () => {
    renderPage()
    await selectFile(makeFile('shot.png', 'image/png', 1000))
    await waitFor(() =>
      expect(screen.getByText('Only .html / .htm files are supported.')).toBeInTheDocument(),
    )
    expect(apiMock).not.toHaveBeenCalledWith('/api/publish', expect.anything())
  })

  test('successful publish shows origin-stripped URL + permanent copy', async () => {
    apiMock.mockResolvedValue({
      json: () => Promise.resolve({ url: 'https://page.x/s/abc', expires_at: null }),
    } as Response)
    renderPage()
    await selectFile(makeFile('report.html', 'text/html', 1024))
    await waitFor(() => expect(screen.getByText('Published')).toBeInTheDocument())
    expect(screen.getByText('page.x/s/abc')).toBeInTheDocument()
    expect(screen.getByText('Link created, valid forever.')).toBeInTheDocument()
    expect(apiMock).toHaveBeenCalledWith('/api/publish', expect.objectContaining({ method: 'POST' }))
  })

  // The expiry line interpolates the date itself. Composing it out of another
  // translated sentence produced "Link created, Expires 2026-01-02." — a
  // capital mid-sentence, in the one line the user reads after publishing.
  test('an expiring publish reads as one sentence', async () => {
    const expiresAt = '2026-08-01T00:00:00Z'
    apiMock.mockResolvedValue({
      json: () => Promise.resolve({ url: 'https://page.x/s/abc', expires_at: expiresAt }),
    } as Response)
    renderPage()
    await selectFile(makeFile('report.html', 'text/html', 1024))
    await waitFor(() =>
      expect(screen.getByText(`Link created, expires ${fmtDate(expiresAt)}.`)).toBeInTheDocument(),
    )
  })

  test('publishErrMsg maps known statuses', () => {
    expect(publishErrMsg(413, t)).toBe('The file is over the 10MB limit. Compress it and try again.')
    expect(publishErrMsg(415, t)).toBe('Only HTML files can be published.')
    expect(publishErrMsg(429, t)).toBe('Too many uploads. Try again shortly.')
    expect(publishErrMsg(401, t)).toBe('Your session expired. Reload the page.')
    expect(publishErrMsg(undefined, t)).toBe('Publishing failed, please try again.')
    // .status flows through from a rejected api() call
    const e = new Error('http 429') as HttpError
    e.status = 429
    expect(publishErrMsg(e.status, t)).toBe('Too many uploads. Try again shortly.')
  })

  test('update mode sends the id field and shows the new version', async () => {
    apiMock.mockResolvedValue({
      json: () =>
        Promise.resolve({
          id: 'abc12345',
          url: 'https://page.example.com/s/abc12345',
          title: 't',
          expires_at: null,
          create_time: '2026-07-22T00:00:00Z',
          version: 3,
        }),
    } as Response)
    renderPage(['/?update=abc12345'])
    await selectFile(makeFile('page.html', 'text/html', 100))
    await waitFor(() => expect(screen.getByText('Published as v3')).toBeInTheDocument())
    const [, opts] = findCall('/api/publish')
    expect(((opts as RequestInit).body as FormData).get('id')).toBe('abc12345')
  })

  test('normal publish sends no id field', async () => {
    apiMock.mockResolvedValue({
      json: () =>
        Promise.resolve({ id: 'x1x1x1x1', url: 'https://e/s/x1x1x1x1', expires_at: null, version: 1 }),
    } as Response)
    renderPage()
    await selectFile(makeFile('page.html', 'text/html', 100))
    await waitFor(() => expect(screen.getByText('Published')).toBeInTheDocument())
    const [, opts] = findCall('/api/publish')
    expect(((opts as RequestInit).body as FormData).get('id')).toBeNull()
  })

  test('update-mode success card: publishing a new page clears ?update and returns to a blank form', async () => {
    apiMock.mockResolvedValue({
      json: () =>
        Promise.resolve({
          id: 'abc12345',
          url: 'https://page.example.com/s/abc12345',
          title: 't',
          expires_at: null,
          create_time: '2026-07-22T00:00:00Z',
          version: 3,
        }),
    } as Response)
    renderPage(['/?update=abc12345'])
    await selectFile(makeFile('page.html', 'text/html', 100))
    await waitFor(() => expect(screen.getByText('Published as v3')).toBeInTheDocument())

    const freshBtn = screen.getByRole('button', { name: 'Publish a new page' })
    await userEvent.click(freshBtn)

    // Back to the blank publish form, no longer in update mode.
    await waitFor(() => expect(screen.getByText('Publish HTML')).toBeInTheDocument())
    expect(screen.queryByText('Update page')).not.toBeInTheDocument()
  })

  test('normal (non-update) success card has no publish-a-new-page button', async () => {
    apiMock.mockResolvedValue({
      json: () =>
        Promise.resolve({ id: 'x1x1x1x1', url: 'https://e/s/x1x1x1x1', expires_at: null, version: 1 }),
    } as Response)
    renderPage()
    await selectFile(makeFile('page.html', 'text/html', 100))
    await waitFor(() => expect(screen.getByText('Published')).toBeInTheDocument())
    expect(screen.queryByRole('button', { name: 'Publish a new page' })).not.toBeInTheDocument()
  })

  test('the default (unpicked) visibility is not appended to the FormData', async () => {
    apiMock.mockResolvedValue({
      json: () =>
        Promise.resolve({ id: 'v1v1v1v1', url: 'https://e/s/v1v1v1v1', expires_at: null, version: 1 }),
    } as Response)
    renderPage()
    await selectFile(makeFile('page.html', 'text/html', 100))
    await waitFor(() => expect(screen.getByText('Published')).toBeInTheDocument())
    const [, opts] = findCall('/api/publish')
    expect(((opts as RequestInit).body as FormData).has('visibility')).toBe(false)
  })

  test('picking Private appends visibility to the FormData', async () => {
    const user = userEvent.setup()
    apiMock.mockResolvedValue({
      json: () =>
        Promise.resolve({ id: 'v2v2v2v2', url: 'https://e/s/v2v2v2v2', expires_at: null, version: 1 }),
    } as Response)
    renderPage()
    await user.click(screen.getByRole('radio', { name: 'Private' }))
    await selectFile(makeFile('page.html', 'text/html', 100))
    await waitFor(() => expect(screen.getByText('Published')).toBeInTheDocument())
    const [, opts] = findCall('/api/publish')
    expect(((opts as RequestInit).body as FormData).get('visibility')).toBe('private')
  })

  test('visibility offers exactly the default, private and link tiers', () => {
    renderPage()
    expect(screen.getByRole('radio', { name: 'Default' })).toBeInTheDocument()
    expect(screen.getByRole('radio', { name: 'Private' })).toBeInTheDocument()
    expect(screen.getByRole('radio', { name: 'Link' })).toBeInTheDocument()
    expect(screen.queryByRole('radio', { name: 'Named people' })).not.toBeInTheDocument()
  })
})

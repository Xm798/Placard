import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ComponentProps } from 'react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { ToastProvider } from '../hooks/useToast'
import ShareDialog, { type ShareDialogFile } from './ShareDialog'

// Mock api(): each test drives responses per path/method, mirroring the
// Header/SettingsPage test convention.
const apiMock = vi.fn()
vi.mock('../lib/api', () => ({
  api: (path: string, opts?: RequestInit) => apiMock(path, opts),
}))

function jsonResp(body: unknown): Response {
  return { json: () => Promise.resolve(body) } as Response
}

const linkFile: ShareDialogFile = { id: 'aaa', title: 'Test page', visibility: 'link' }

function renderDialog(props: Partial<ComponentProps<typeof ShareDialog>> = {}) {
  const onVisibilityChange = vi.fn()
  const onShareCodeChange = vi.fn()
  const onOpenChange = vi.fn()
  const utils = render(
    <ToastProvider>
      <ShareDialog
        open
        onOpenChange={onOpenChange}
        file={linkFile}
        onVisibilityChange={onVisibilityChange}
        onShareCodeChange={onShareCodeChange}
        {...props}
      />
    </ToastProvider>,
  )
  return { ...utils, onVisibilityChange, onShareCodeChange, onOpenChange }
}

beforeEach(() => {
  apiMock.mockReset()
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('ShareDialog', () => {
  test('picking a tier PATCHes immediately and calls back onVisibilityChange', async () => {
    const user = userEvent.setup()
    apiMock.mockImplementation(() => Promise.resolve(jsonResp({})))
    const { onVisibilityChange } = renderDialog()

    await user.click(screen.getByRole('radio', { name: 'Private' }))

    await waitFor(() =>
      expect(apiMock).toHaveBeenCalledWith(
        '/api/files/aaa',
        expect.objectContaining({
          method: 'PATCH',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ visibility: 'private' }),
        }),
      ),
    )
    await waitFor(() => expect(onVisibilityChange).toHaveBeenCalledWith('aaa', 'private'))
  })

  test('a failed PATCH rolls the selection back', async () => {
    const user = userEvent.setup()
    apiMock.mockImplementation((_path: string, opts?: RequestInit) => {
      if (opts?.method === 'PATCH') return Promise.reject(new Error('http 500'))
      return Promise.resolve(jsonResp({}))
    })
    renderDialog()

    await user.click(screen.getByRole('radio', { name: 'Private' }))

    await waitFor(() =>
      expect(screen.getByRole('radio', { name: 'Link' })).toHaveAttribute('aria-checked', 'true'),
    )
    expect(screen.getByRole('radio', { name: 'Private' })).toHaveAttribute('aria-checked', 'false')
  })

  test('visibility offers exactly the private and link tiers', () => {
    apiMock.mockImplementation(() => Promise.resolve(jsonResp({})))
    renderDialog()

    expect(screen.getByRole('radio', { name: 'Private' })).toBeInTheDocument()
    expect(screen.getByRole('radio', { name: 'Link' })).toBeInTheDocument()
    expect(screen.queryByRole('radio', { name: 'Named people' })).not.toBeInTheDocument()
  })

  // The one-word "Link" label does not say the page is world-readable; the
  // hint under the picker is the only place the user is told.
  test('the visibility hint spells out that Link needs no account', async () => {
    renderDialog()
    expect(
      await screen.findByText(/Anyone holding the link can open it without signing in/),
    ).toBeInTheDocument()
  })

  test('the visibility hint follows the selection to Private', async () => {
    apiMock.mockResolvedValue(jsonResp({}))
    const user = userEvent.setup()
    renderDialog()
    await user.click(screen.getByRole('radio', { name: 'Private' }))
    expect(await screen.findByText(/Only you can open it/)).toBeInTheDocument()
  })

  test('with no code only Generate shows, and it renders what the server returned', async () => {
    const user = userEvent.setup()
    apiMock.mockResolvedValue(jsonResp({ share_code: '042195' }))
    const { onShareCodeChange } = renderDialog()

    expect(screen.queryByRole('button', { name: 'Clear' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Copy' })).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Generate' }))

    await waitFor(() =>
      expect(apiMock).toHaveBeenCalledWith('/api/files/aaa/share-code', { method: 'POST' }),
    )
    expect(await screen.findByTestId('share-code')).toHaveTextContent('042195')
    expect(onShareCodeChange).toHaveBeenCalledWith('aaa', '042195')
  })

  // The dialog must never invent or remember a code of its own: what it shows
  // comes from the owner's file list, and clearing it empties the display.
  test('an existing code can be copied, regenerated and cleared', async () => {
    const user = userEvent.setup()
    apiMock.mockResolvedValue(jsonResp({}))
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('navigator', { ...navigator, clipboard: { writeText } })
    const { onShareCodeChange } = renderDialog({
      file: { ...linkFile, share_code: '042195' },
    })

    expect(screen.getByTestId('share-code')).toHaveTextContent('042195')
    expect(screen.getByRole('button', { name: 'Regenerate' })).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Copy' }))
    await waitFor(() => expect(writeText).toHaveBeenCalledWith('042195'))

    await user.click(screen.getByRole('button', { name: 'Clear' }))
    await waitFor(() =>
      expect(apiMock).toHaveBeenCalledWith('/api/files/aaa/share-code', { method: 'DELETE' }),
    )
    expect(await screen.findByTestId('share-code')).toHaveTextContent('——————')
    expect(onShareCodeChange).toHaveBeenCalledWith('aaa', '')
  })

  test('a failed regenerate leaves the displayed code alone', async () => {
    const user = userEvent.setup()
    apiMock.mockRejectedValue(new Error('http 500'))
    const { onShareCodeChange } = renderDialog({
      file: { ...linkFile, share_code: '042195' },
    })

    const regenerate = screen.getByRole('button', { name: 'Regenerate' })
    await user.click(regenerate)

    await waitFor(() => expect(regenerate).toBeEnabled())
    expect(screen.getByTestId('share-code')).toHaveTextContent('042195')
    expect(onShareCodeChange).not.toHaveBeenCalled()
  })
})

import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { ToastProvider, ToastViewport } from '../hooks/useToast'
import i18n from '../i18n'
import zhCN from '../i18n/locales/zh-CN'
import FilesPage from './FilesPage'

const twoFiles = () => [
  {
    id: 'aaa',
    title: 'First page',
    url: 'https://page.example.com/s/aaa',
    create_time: '2026-07-20T10:30:00Z',
    expires_at: null,
    view_count: 5,
    latest_version: 2,
    shared_version: 0,
    visibility: 'link',
  },
  {
    id: 'bbb',
    title: 'Second page',
    url: 'https://page.example.com/s/bbb',
    create_time: '2026-07-21T08:15:00Z',
    expires_at: '2026-08-01T00:00:00Z',
    view_count: 12,
    latest_version: 1,
    shared_version: 0,
    visibility: 'private',
  },
]

const initialVersions = () => ({
  latest_version: 2,
  shared_version: 0,
  versions: [
    { version: 2, title: 'v2 title', size_bytes: 100, create_time: '2026-07-21T08:15:00Z' },
    { version: 1, title: 'v1 title', size_bytes: 90, create_time: '2026-07-20T10:30:00Z' },
  ],
})

let filesResponse: { page: number; total: number; files: ReturnType<typeof twoFiles> } = {
  page: 1,
  total: 0,
  files: [],
}
// Server-side state for file `aaa`'s version endpoint. PATCH/restore mutate
// this so the subsequent GET /versions the app issues reflects the real
// post-request server truth, letting tests assert against refetched UI
// instead of an optimistic guess.
let versionsResponse = initialVersions()
// Toggled per-test to exercise the PATCH-failure recovery path (Minor 5 / Important 1).
let patchShouldFail = false

const apiMock = vi.fn((path: string, opts?: RequestInit) => {
  if (opts?.method === 'DELETE') {
    return Promise.resolve({} as Response)
  }
  if (opts?.method === 'PATCH') {
    if (patchShouldFail) {
      return Promise.reject(new Error('patch failed'))
    }
    const body = JSON.parse((opts.body as string) ?? '{}') as { shared_version: number }
    versionsResponse = { ...versionsResponse, shared_version: body.shared_version }
    filesResponse = {
      ...filesResponse,
      files: filesResponse.files.map((f) =>
        f.id === 'aaa' ? { ...f, shared_version: body.shared_version } : f,
      ),
    }
    return Promise.resolve({} as Response)
  }
  if (/\/versions\/\d+\/restore$/.test(path)) {
    const restored = Number(path.match(/\/versions\/(\d+)\/restore$/)?.[1])
    const newLatest = versionsResponse.latest_version + 1
    versionsResponse = {
      latest_version: newLatest,
      shared_version: versionsResponse.shared_version,
      versions: [
        {
          version: newLatest,
          title: 'restored from v' + restored,
          size_bytes: 90,
          create_time: '2026-07-22T00:00:00Z',
        },
        ...versionsResponse.versions,
      ],
    }
    filesResponse = {
      ...filesResponse,
      files: filesResponse.files.map((f) => (f.id === 'aaa' ? { ...f, latest_version: newLatest } : f)),
    }
    return Promise.resolve({} as Response)
  }
  if (/\/versions$/.test(path)) {
    return Promise.resolve({ json: () => Promise.resolve(versionsResponse) } as Response)
  }
  if (path.startsWith('/api/files')) {
    return Promise.resolve({ json: () => Promise.resolve(filesResponse) } as Response)
  }
  return Promise.resolve({ json: () => Promise.resolve({}) } as Response)
})

vi.mock('../lib/api', () => ({
  api: (path: string, opts?: RequestInit) => apiMock(path, opts),
}))

function renderPage() {
  return render(
    <MemoryRouter>
      <ToastProvider>
        <FilesPage />
        <ToastViewport />
      </ToastProvider>
    </MemoryRouter>,
  )
}

beforeEach(() => {
  apiMock.mockClear()
  filesResponse = { page: 1, total: 0, files: [] }
  versionsResponse = initialVersions()
  patchShouldFail = false
})

afterEach(async () => {
  vi.restoreAllMocks()
  await i18n.changeLanguage('en')
})

describe('FilesPage', () => {
  test('total 0 renders the empty state', async () => {
    renderPage()
    await waitFor(() => expect(screen.getByText('No files yet')).toBeInTheDocument())
    expect(screen.getByText('0 files in total')).toBeInTheDocument()
  })

  test('renders 2 rows with titles + origin-stripped urls', async () => {
    filesResponse = { page: 1, total: 2, files: twoFiles() }
    renderPage()
    await waitFor(() => expect(screen.getByText('First page')).toBeInTheDocument())
    expect(screen.getByText('Second page')).toBeInTheDocument()
    // origin-stripped (no https://)
    expect(screen.getByText('page.example.com/s/aaa')).toBeInTheDocument()
    expect(screen.getByText('page.example.com/s/bbb')).toBeInTheDocument()
    expect(screen.getByText('2 files in total')).toBeInTheDocument()
  })

  test('pager label reads the range and total, and next is disabled', async () => {
    filesResponse = { page: 1, total: 2, files: twoFiles() }
    renderPage()
    await waitFor(() => expect(screen.getByText('Showing 1–2 of 2')).toBeInTheDocument())
    expect(screen.getByRole('button', { name: 'Next page' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Previous page' })).toBeDisabled()
  })

  test('delete flow: confirm→true, DELETE /api/files/<id>', async () => {
    const user = userEvent.setup()
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    filesResponse = { page: 1, total: 2, files: twoFiles() }
    renderPage()
    await waitFor(() => expect(screen.getByText('First page')).toBeInTheDocument())

    const firstRow = screen.getByText('First page').closest('.row') as HTMLElement
    await user.click(within(firstRow).getByRole('button', { name: 'Delete: First page' }))

    await waitFor(() =>
      expect(apiMock).toHaveBeenCalledWith('/api/files/aaa', { method: 'DELETE' }),
    )
  })

  test('the version button expands the history panel with restore on old versions', async () => {
    filesResponse = { page: 1, total: 2, files: twoFiles() }
    renderPage()
    await screen.findByText('First page')
    await userEvent.click(screen.getByRole('button', { name: 'Version history: First page' }))
    await screen.findByText('v1 title')
    expect(screen.getByText('v2 title')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Restore v1 as the latest version' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Restore v2 as the latest version' })).not.toBeInTheDocument()
  })

  test('pin selector PATCHes shared_version and reflects the refetched server state', async () => {
    filesResponse = { page: 1, total: 2, files: twoFiles() }
    renderPage()
    await screen.findByText('First page')
    await userEvent.click(screen.getByRole('button', { name: 'Version history: First page' }))
    await screen.findByText('v1 title')

    const versionsCallsBefore = apiMock.mock.calls.filter((c) => /\/versions$/.test(c[0] as string)).length

    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Shared version' }), '1')
    await waitFor(() => {
      expect(apiMock).toHaveBeenCalledWith(
        '/api/files/aaa',
        expect.objectContaining({ method: 'PATCH', body: JSON.stringify({ shared_version: 1 }) }),
      )
    })

    // PATCH success must trigger a re-GET of /versions (server truth, not an optimistic guess).
    await waitFor(() => {
      const versionsCallsAfter = apiMock.mock.calls.filter((c) => /\/versions$/.test(c[0] as string)).length
      expect(versionsCallsAfter).toBeGreaterThan(versionsCallsBefore)
    })

    // The refetched data drives the controlled <select> value...
    await waitFor(() => {
      expect(screen.getByRole('combobox', { name: 'Shared version' })).toHaveValue('1')
    })
    // ...and the row badge, both sourced from the post-request GETs.
    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Version history: First page' })).toHaveTextContent('pinned v1')
    })
  })

  test('pin failure: PATCH rejects → toast shown and select snaps back to server value', async () => {
    patchShouldFail = true
    filesResponse = { page: 1, total: 2, files: twoFiles() }
    renderPage()
    await screen.findByText('First page')
    await userEvent.click(screen.getByRole('button', { name: 'Version history: First page' }))
    await screen.findByText('v1 title')

    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Shared version' }), '1')

    await waitFor(() => expect(screen.getByText('Could not set the shared version')).toBeInTheDocument())
    // Server-side shared_version never changed (PATCH rejected) — the
    // controlled select must revert to it, not stay on the clicked option.
    await waitFor(() => {
      expect(screen.getByRole('combobox', { name: 'Shared version' })).toHaveValue('0')
    })
    expect(screen.getByRole('button', { name: 'Version history: First page' })).not.toHaveTextContent('pinned v1')
  })

  test('restore posts to the restore endpoint and reflects the new latest_version', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    filesResponse = { page: 1, total: 2, files: twoFiles() }
    renderPage()
    await screen.findByText('First page')
    await userEvent.click(screen.getByRole('button', { name: 'Version history: First page' }))
    await screen.findByText('v1 title')
    await userEvent.click(screen.getByRole('button', { name: 'Restore v1 as the latest version' }))
    await waitFor(() => {
      expect(apiMock).toHaveBeenCalledWith(
        '/api/files/aaa/versions/1/restore',
        expect.objectContaining({ method: 'POST' }),
      )
    })

    // The row badge picks up the bumped latest_version from the refetched files list.
    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Version history: First page' })).toHaveTextContent('v3')
    })
    // The new version shows up as "Latest" in the refetched panel, and the
    // previously-latest v2 now exposes a restore button.
    await screen.findByText('restored from v1')
    expect(screen.getByRole('button', { name: 'Restore v2 as the latest version' })).toBeInTheDocument()
  })

  test('the share button opens ShareDialog seeded with the row visibility', async () => {
    filesResponse = { page: 1, total: 2, files: twoFiles() }
    renderPage()
    await screen.findByText('First page')

    await userEvent.click(screen.getByRole('button', { name: 'Share settings: First page' }))

    const dialog = await screen.findByRole('dialog', { name: 'Share settings' })
    expect(within(dialog).getByText('First page')).toBeInTheDocument()
    // twoFiles()[0].visibility is 'link' — its option should be the checked radio.
    expect(within(dialog).getByRole('radio', { name: 'Link' })).toHaveAttribute('aria-checked', 'true')
  })

  // The list is paginated and the loader is memoised, so anything that changes
  // the loader's identity refetches — and lands the reader back on page 1. A
  // language switch must retranslate the page and nothing else.
  test('switching language retranslates without refetching the list', async () => {
    filesResponse = { page: 1, total: 2, files: twoFiles() }
    renderPage()
    await screen.findByText('First page')
    apiMock.mockClear()

    await act(() => i18n.changeLanguage('zh-CN'))

    await screen.findByRole('heading', { name: zhCN.files.title })
    expect(apiMock).not.toHaveBeenCalled()
  })

  // English needs the singular; the count is interpolated, so "1 files" is the
  // shape this gets wrong.
  test('a single file reads in the singular', async () => {
    filesResponse = { page: 1, total: 1, files: [twoFiles()[0]] }
    renderPage()
    await waitFor(() => expect(screen.getByText('1 file in total')).toBeInTheDocument())
  })

  test('update navigates to the publish page with the update param', async () => {
    filesResponse = { page: 1, total: 2, files: twoFiles() }
    render(
      <MemoryRouter initialEntries={['/files']}>
        <ToastProvider>
          <Routes>
            <Route path="/files" element={<FilesPage />} />
            <Route path="/" element={<div>PUBLISH-PAGE-PROBE</div>} />
          </Routes>
        </ToastProvider>
      </MemoryRouter>,
    )
    await screen.findByText('First page')
    await userEvent.click(screen.getByRole('button', { name: 'Update: First page' }))
    expect(await screen.findByText('PUBLISH-PAGE-PROBE')).toBeInTheDocument()
  })
})

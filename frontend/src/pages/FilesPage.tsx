// "My files" page. Ported 1:1 from internal/web/app.html:301-334 + app.js:229-328.

import { useCallback, useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import i18n from '../i18n'
import { api } from '../lib/api'
import { useToast } from '../hooks/useToast'
import { useCopy } from '../hooks/useCopy'
import FileRow, { type FileItem } from '../components/FileRow'
import { type VersionsData } from '../components/VersionPanel'
import ShareDialog, { type ShareDialogFile } from '../components/ShareDialog'

const PAGE_SIZE = 20

interface FilesState {
  page: number
  total: number
  items: FileItem[]
}

export default function FilesPage() {
  const navigate = useNavigate()
  const { show } = useToast()
  const { t } = useTranslation()
  const copy = useCopy()
  const [state, setState] = useState<FilesState>({ page: 1, total: 0, items: [] })
  const [expandedId, setExpandedId] = useState<string | null>(null)
  const [versions, setVersions] = useState<VersionsData | null>(null)
  // id of the row with a pin/restore request in flight (guards re-entrant clicks
  // and disables the select/button until the post-request refetch settles).
  const [busyId, setBusyId] = useState<string | null>(null)
  const [shareFile, setShareFile] = useState<ShareDialogFile | null>(null)
  const [shareOpen, setShareOpen] = useState(false)

  // A failure toast is written at the moment it is shown, so it reads the active
  // language off the i18n instance rather than through the hook's `t`. That keeps
  // `t` — whose identity changes on every language switch — out of the loader's
  // dependencies, where it would refetch and reset the page under a reader who
  // only changed language.
  const loadFiles = useCallback(
    (page: number) => {
      api('/api/files?page=' + page + '&page_size=' + PAGE_SIZE)
        .then((r) => r.json())
        .then((data) => {
          setState({ page: data.page, total: data.total, items: data.files || [] })
        })
        .catch(() => {
          show(i18n.t('files.loadFailed'))
        })
    },
    [show],
  )

  useEffect(() => {
    loadFiles(1)
  }, [loadFiles])

  function copyFile(id: string) {
    const f = state.items.find((x) => x.id === id)
    if (!f) return
    copy(f.url, t('files.copied'))
  }

  function deleteFile(id: string) {
    if (!confirm(t('files.deleteConfirm'))) return
    api('/api/files/' + encodeURIComponent(id), { method: 'DELETE' })
      .then(() => {
        show(t('files.deleted'))
        // After removing one row, recompute the last valid page so deleting the
        // last item on the last page doesn't strand the user on an empty page.
        const remaining = Math.max(0, state.total - 1)
        const lastPage = Math.max(1, Math.ceil(remaining / PAGE_SIZE))
        loadFiles(Math.min(state.page, lastPage))
      })
      .catch(() => show(t('files.deleteFailed')))
  }

  // Returns the fetch promise so callers (pinVersion/restoreVersion) can chain
  // on it — the server is the source of truth, so both the success and error
  // paths refetch before releasing the busy lock.
  function loadVersions(id: string) {
    return api('/api/files/' + encodeURIComponent(id) + '/versions')
      .then((r) => r.json())
      .then((d) => setVersions(d))
      .catch(() => show(t('files.versionsLoadFailed')))
  }

  function toggleVersions(id: string) {
    if (expandedId === id) {
      setExpandedId(null)
      setVersions(null)
      return
    }
    setExpandedId(id)
    setVersions(null)
    loadVersions(id)
  }

  function updateFile(id: string) {
    navigate('/?update=' + encodeURIComponent(id))
  }

  function shareFileOpen(id: string) {
    const f = state.items.find((x) => x.id === id)
    if (!f) return
    setShareFile({ id: f.id, title: f.title, visibility: f.visibility, share_code: f.share_code })
    setShareOpen(true)
  }

  // ShareDialog PATCHes visibility itself — this just reflects the new value
  // into the row badge without a full refetch.
  function applyVisibilityChange(id: string, visibility: string) {
    setState((prev) => ({
      ...prev,
      items: prev.items.map((f) => (f.id === id ? { ...f, visibility } : f)),
    }))
  }

  // Same shape for the share code: keeping the list row in step is what lets
  // the dialog show the current code when it is reopened without a refetch.
  function applyShareCodeChange(id: string, share_code: string) {
    setState((prev) => ({
      ...prev,
      items: prev.items.map((f) => (f.id === id ? { ...f, share_code } : f)),
    }))
  }

  // Shared by pinVersion/restoreVersion: sets the busy lock, fires the
  // request, and refetches server state (files + versions) on both success
  // and failure — the busy lock is released only once those refetches
  // settle, and only after the request starts (request is a thunk so the
  // caller's pre-flight guard/confirm still runs before setBusyId).
  function runVersionAction(id: string, request: () => Promise<Response>, successMsg: string, failMsg: string) {
    setBusyId(id)
    request()
      .then(() => {
        show(successMsg)
        loadFiles(state.page)
        return loadVersions(id)
      })
      .catch(() => {
        show(failMsg)
        // Refetch so the (controlled) select/badge snaps back to the actual
        // server state instead of sticking on the optimistic guess.
        return loadVersions(id)
      })
      .finally(() => setBusyId(null))
  }

  function pinVersion(id: string, v: number) {
    if (busyId === id) return // request already in flight for this row
    runVersionAction(
      id,
      () =>
        api('/api/files/' + encodeURIComponent(id), {
          method: 'PATCH',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ shared_version: v }),
        }),
      v === 0 ? t('files.pinFollow') : t('files.pinned', { version: v }),
      t('files.pinFailed'),
    )
  }

  function restoreVersion(id: string, v: number) {
    if (busyId === id) return // request already in flight for this row
    if (!confirm(t('files.restoreConfirm', { version: v }))) return
    runVersionAction(
      id,
      () => api('/api/files/' + encodeURIComponent(id) + '/versions/' + v + '/restore', { method: 'POST' }),
      t('files.restored'),
      t('files.restoreFailed'),
    )
  }

  const isEmpty = state.total === 0
  const start = state.total === 0 ? 0 : (state.page - 1) * PAGE_SIZE + 1
  const end = Math.min(state.page * PAGE_SIZE, state.total)
  const totalPages = Math.max(1, Math.ceil(state.total / PAGE_SIZE))

  return (
    <section id="page-files">
      <div className="pt-12 pb-8">
        <div className="flex items-end justify-between">
          <div>
            <h1 className="font-display font-extrabold text-4xl sm:text-5xl tracking-[-0.02em] mb-2">
              {t('files.title')}
            </h1>
            <p className="text-soft">{t('files.count', { count: state.total })}</p>
          </div>
        </div>
      </div>

      {isEmpty ? (
        <div className="rounded-2xl border border-line surface px-8 py-20 text-center">
          <div
            className="mx-auto w-12 h-12 rounded-xl border border-line-strong flex items-center justify-center mb-5"
            aria-hidden="true"
          >
            <svg
              className="w-6 h-6"
              style={{ color: 'var(--fg-faint)' }}
              fill="none"
              viewBox="0 0 24 24"
              stroke="currentColor"
              strokeWidth="1.5"
            >
              <path d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1M16 7l-4-4m0 0L8 7m4-4v12" />
            </svg>
          </div>
          <p className="font-display font-semibold text-lg mb-1.5">{t('files.empty.title')}</p>
          <p className="text-soft text-sm mb-6">{t('files.empty.sub')}</p>
          <button className="btn-ink rounded-lg text-sm font-medium px-5 py-2.5" onClick={() => navigate('/')}>
            {t('files.empty.cta')}
          </button>
        </div>
      ) : (
        <>
          <div className="rounded-2xl border border-line overflow-hidden surface">
            <div>
              {state.items.map((f, i) => (
                <FileRow
                  key={f.id}
                  file={f}
                  isLast={i === state.items.length - 1}
                  expanded={expandedId === f.id}
                  versions={expandedId === f.id ? versions : null}
                  busy={busyId === f.id}
                  onCopy={copyFile}
                  onDelete={deleteFile}
                  onUpdate={updateFile}
                  onToggleVersions={toggleVersions}
                  onPin={pinVersion}
                  onRestore={restoreVersion}
                  onShare={shareFileOpen}
                />
              ))}
            </div>
          </div>

          <div className="flex items-center justify-between mt-5 text-sm text-soft">
            <span>{t('files.pager', { start, end, total: state.total })}</span>
            <div className="flex gap-2">
              <button
                className="btn-outline rounded-lg px-3 py-1.5"
                aria-label={t('common.prevPage')}
                disabled={state.page <= 1}
                onClick={() => {
                  if (state.page > 1) loadFiles(state.page - 1)
                }}
              >
                {t('common.prevPage')}
              </button>
              <button
                className="btn-outline rounded-lg px-3 py-1.5"
                aria-label={t('common.nextPage')}
                disabled={state.page >= totalPages}
                onClick={() => loadFiles(state.page + 1)}
              >
                {t('common.nextPage')}
              </button>
            </div>
          </div>
        </>
      )}

      <ShareDialog
        open={shareOpen}
        onOpenChange={setShareOpen}
        file={shareFile}
        onVisibilityChange={applyVisibilityChange}
        onShareCodeChange={applyShareCodeChange}
      />
    </section>
  )
}

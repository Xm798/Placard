// Admin page: the account table and the instance settings an admin edits without
// a restart. Every control here mirrors an /api/admin endpoint, and the server
// is the authority on each one — the greyed-out buttons only spare the operator
// a request the server would refuse.

import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import i18n from '../i18n'
import { api, type HttpError } from '../lib/api'
import { useToast } from '../hooks/useToast'
import AdminUserRow, { type AdminUser } from '../components/AdminUserRow'
import NotFoundPage from './NotFoundPage'

const PAGE_SIZE = 20
const MEGABYTE = 1024 * 1024

interface UsersState {
  page: number
  total: number
  self: string
  items: AdminUser[]
}

interface Settings {
  registration_open: boolean
  oidc_auto_provision: boolean
  upload_max_file_size: number
}

// The page is hidden from anyone the admin API refuses, rather than shown with
// empty tables: a non-admin who types the URL gets the same body an unknown
// route gets.
type Access = 'pending' | 'granted' | 'denied'

export default function AdminPage() {
  const { show } = useToast()
  const { t } = useTranslation()
  const [access, setAccess] = useState<Access>('pending')
  const [state, setState] = useState<UsersState>({ page: 1, total: 0, self: '', items: [] })
  const [settings, setSettings] = useState<Settings | null>(null)
  const [uploadMB, setUploadMB] = useState('')
  const [busyId, setBusyId] = useState<string | null>(null)

  // A failure toast is written at the moment it is shown, so it reads the active
  // language off the i18n instance rather than through the hook's `t`. That keeps
  // `t` — whose identity changes on every language switch — out of the loader's
  // dependencies, where it would refetch and reset the page under a reader who
  // only changed language.
  const loadUsers = useCallback(
    (page: number) => {
      return api('/api/admin/users?page=' + page + '&page_size=' + PAGE_SIZE)
        .then((r) => r.json())
        .then((data) => {
          setAccess('granted')
          setState({ page: data.page, total: data.total, self: data.self || '', items: data.users || [] })
        })
        .catch((e: HttpError) => {
          if (e.status === 403) {
            setAccess('denied')
            return
          }
          // Only a 403 hides the page. Any other failure still renders it, so
          // the operator sees the settings card and the toast rather than a
          // blank frame that never resolves.
          setAccess('granted')
          show(i18n.t('admin.usersLoadFailed'))
        })
    },
    [show],
  )

  const loadSettings = useCallback(() => {
    api('/api/admin/settings')
      .then((r) => r.json())
      .then((data: Settings) => {
        setSettings(data)
        setUploadMB(String(data.upload_max_file_size / MEGABYTE))
      })
      .catch((e: HttpError) => {
        if (e.status !== 403) show(i18n.t('admin.settingsLoadFailed'))
      })
  }, [show])

  useEffect(() => {
    loadUsers(1)
    loadSettings()
  }, [loadUsers, loadSettings])

  // Every account mutation refetches rather than patching the row in place: the
  // server decides what the account became, and a second admin may have changed
  // it in the meantime.
  function runUserAction(
    user: AdminUser,
    request: () => Promise<Response>,
    successMsg: string,
    // What a 400 means for this call. Only the flag and delete endpoints can
    // answer 400 for the self-lockout guard; a password reset answers 400 for a
    // password the server rejected, which is a different thing to say.
    badRequestMsg = t('admin.notSelf'),
  ) {
    if (busyId) return
    setBusyId(user.id)
    request()
      .then(() => {
        show(successMsg)
        return loadUsers(state.page)
      })
      .catch((e: HttpError) => {
        show(e.status === 400 ? badRequestMsg : t('admin.actionFailed'))
        return loadUsers(state.page)
      })
      .finally(() => setBusyId(null))
  }

  function patchUser(user: AdminUser, body: Record<string, boolean>, successMsg: string) {
    runUserAction(
      user,
      () =>
        api('/api/admin/users/' + encodeURIComponent(user.id), {
          method: 'PATCH',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(body),
        }),
      successMsg,
    )
  }

  function toggleDisabled(user: AdminUser) {
    const disable = !user.disabled
    if (disable && !confirm(t('admin.disableConfirm', { name: user.username }))) return
    patchUser(
      user,
      { disabled: disable },
      disable ? t('admin.disabled', { name: user.username }) : t('admin.enabled', { name: user.username }),
    )
  }

  function toggleAdmin(user: AdminUser) {
    const grant = !user.is_admin
    if (!grant && !confirm(t('admin.demoteConfirm', { name: user.username }))) return
    patchUser(
      user,
      { is_admin: grant },
      grant ? t('admin.promoted', { name: user.username }) : t('admin.demoted', { name: user.username }),
    )
  }

  function resetPassword(user: AdminUser) {
    const pw = prompt(t('admin.passwordPrompt', { name: user.username }))
    if (pw == null) return
    if (pw.length < 8) {
      show(t('admin.passwordTooShort'))
      return
    }
    runUserAction(
      user,
      () =>
        api('/api/admin/users/' + encodeURIComponent(user.id) + '/password', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ password: pw }),
        }),
      t('admin.passwordReset', { name: user.username }),
      t('admin.passwordRefused'),
    )
  }

  function deleteUser(user: AdminUser) {
    if (!confirm(t('admin.deleteConfirm', { name: user.username }))) return
    runUserAction(
      user,
      () => api('/api/admin/users/' + encodeURIComponent(user.id), { method: 'DELETE' }),
      t('admin.userDeleted', { name: user.username }),
    )
  }

  function patchSettings(body: Partial<Settings>, successMsg: string) {
    api('/api/admin/settings', {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    })
      .then((r) => r.json())
      .then((data: Settings) => {
        setSettings(data)
        setUploadMB(String(data.upload_max_file_size / MEGABYTE))
        show(successMsg)
      })
      .catch(() => show(t('admin.settingsSaveFailed')))
  }

  function saveUploadLimit() {
    const mb = Number(uploadMB)
    if (!Number.isFinite(mb) || mb <= 0) {
      show(t('admin.uploadLimitPositive'))
      return
    }
    patchSettings({ upload_max_file_size: Math.round(mb * MEGABYTE) }, t('admin.uploadLimitUpdated'))
  }

  if (access === 'denied') return <NotFoundPage />
  if (access === 'pending') return null

  const totalPages = Math.max(1, Math.ceil(state.total / PAGE_SIZE))

  return (
    <section>
      <div className="pt-12 pb-8">
        <h1 className="font-display font-extrabold text-4xl sm:text-5xl tracking-[-0.02em] mb-2">
          {t('admin.title')}
        </h1>
        <p className="text-soft">{t('admin.sub')}</p>
      </div>

      {settings && (
        <div className="rounded-2xl border border-line surface p-6 mb-6">
          <div className="max-w-md mb-4">
            <h2 className="font-display font-bold text-lg mb-1">{t('admin.settingsTitle')}</h2>
            <p className="text-soft text-sm leading-relaxed">{t('admin.settingsDesc')}</p>
          </div>

          <div className="flex flex-col gap-3">
            <label className="flex items-center justify-between gap-4 rounded-lg border border-line px-4 py-3">
              <span className="text-sm">
                {t('admin.registrationOpen')}
                <span className="block text-faint text-xs mt-0.5">{t('admin.registrationOpenHint')}</span>
              </span>
              <input
                type="checkbox"
                checked={settings.registration_open}
                onChange={(e) =>
                  patchSettings(
                    { registration_open: e.target.checked },
                    e.target.checked ? t('admin.registrationOpened') : t('admin.registrationClosed'),
                  )
                }
              />
            </label>

            <label className="flex items-center justify-between gap-4 rounded-lg border border-line px-4 py-3">
              <span className="text-sm">
                {t('admin.autoProvision')}
                <span className="block text-faint text-xs mt-0.5">{t('admin.autoProvisionHint')}</span>
              </span>
              <input
                type="checkbox"
                checked={settings.oidc_auto_provision}
                onChange={(e) =>
                  patchSettings(
                    { oidc_auto_provision: e.target.checked },
                    e.target.checked ? t('admin.autoProvisionOn') : t('admin.autoProvisionOff'),
                  )
                }
              />
            </label>

            <div className="flex items-center justify-between gap-4 rounded-lg border border-line px-4 py-3">
              <label className="text-sm" htmlFor="upload-limit">
                {t('admin.uploadLimit')}
                <span className="block text-faint text-xs mt-0.5">{t('admin.uploadLimitHint')}</span>
              </label>
              <div className="flex items-center gap-2 shrink-0">
                <input
                  id="upload-limit"
                  type="number"
                  min="1"
                  step="1"
                  value={uploadMB}
                  onChange={(e) => setUploadMB(e.target.value)}
                  className="w-24 rounded-lg border border-line-strong px-3 py-1.5 text-sm bg-transparent"
                />
                <button onClick={saveUploadLimit} className="text-sm px-3 py-1.5 rounded-lg border border-line-strong">
                  {t('common.save')}
                </button>
              </div>
            </div>
          </div>
        </div>
      )}

      <div className="flex items-end justify-between mb-3">
        <h2 className="font-display font-bold text-lg">{t('admin.accounts')}</h2>
        <span className="text-soft text-sm">{t('admin.accountCount', { count: state.total })}</span>
      </div>

      <div className="rounded-2xl border border-line overflow-hidden surface">
        {state.items.map((u, i) => (
          <AdminUserRow
            key={u.id}
            user={u}
            isLast={i === state.items.length - 1}
            isSelf={u.id === state.self}
            busy={busyId !== null}
            onToggleDisabled={toggleDisabled}
            onToggleAdmin={toggleAdmin}
            onResetPassword={resetPassword}
            onDelete={deleteUser}
          />
        ))}
      </div>

      {totalPages > 1 && (
        <div className="flex items-center justify-end gap-2 mt-5 text-sm text-soft">
          <button
            className="btn-outline rounded-lg px-3 py-1.5"
            aria-label={t('common.prevPage')}
            disabled={state.page <= 1}
            onClick={() => loadUsers(state.page - 1)}
          >
            {t('common.prevPage')}
          </button>
          <button
            className="btn-outline rounded-lg px-3 py-1.5"
            aria-label={t('common.nextPage')}
            disabled={state.page >= totalPages}
            onClick={() => loadUsers(state.page + 1)}
          >
            {t('common.nextPage')}
          </button>
        </div>
      )}
    </section>
  )
}

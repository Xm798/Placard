import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import i18n from '../i18n'
import { api, type HttpError } from '../lib/api'
import { useToast } from '../hooks/useToast'
import { Field } from '../pages/AuthShell'
import Panel from './Panel'

interface Profile {
  username: string
  email?: string
  has_password: boolean
}

// Mirrors account.PasswordMinLen; the server is still the one that decides.
const PASSWORD_MIN_LEN = 8

function errorKey(status: number | undefined): string {
  switch (status) {
    case 403:
      return 'account.wrongCurrent'
    case 429:
      return 'account.rateLimited'
    case 400:
      return 'account.refused'
    default:
      return 'account.failed'
  }
}

export default function AccountSection({ index = 1 }: { index?: number }) {
  const { show } = useToast()
  const { t } = useTranslation()
  const [profile, setProfile] = useState<Profile | null>(null)
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [confirm, setConfirm] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  // Reads the toast text off the i18n instance for the reason SettingsPage's
  // loaders give: `t` in the dependencies would refetch on a language switch.
  const load = useCallback(() => {
    api('/api/account')
      .then((r) => r.json())
      .then((body: Profile) => setProfile(body))
      .catch(() => show(i18n.t('account.loadFailed')))
  }, [show])

  useEffect(() => {
    load()
  }, [load])

  function submit(e: FormEvent) {
    e.preventDefault()
    if (!profile) return
    if (next.length < PASSWORD_MIN_LEN) return setError(t('account.tooShort'))
    if (next !== confirm) return setError(t('account.mismatch'))

    setBusy(true)
    setError('')
    api('/api/account/password', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ current_password: current, new_password: next }),
    })
      .then(() => {
        setCurrent('')
        setNext('')
        setConfirm('')
        show(t('account.changed'))
        setProfile({ ...profile, has_password: true })
      })
      .catch((err: HttpError) => setError(t(errorKey(err.status))))
      .finally(() => setBusy(false))
  }

  if (!profile) return null

  return (
    <Panel index={index} title={t('account.title')} tone="accent">
      <dl className="grid gap-x-6 gap-y-3 px-5 py-5 sm:grid-cols-[auto_minmax(0,1fr)] text-sm">
        <dt className="text-soft">{t('account.username')}</dt>
        <dd className="mono break-all">{profile.username}</dd>
        <dt className="text-soft">{t('account.email')}</dt>
        <dd className={profile.email ? 'mono break-all' : 'text-faint'}>{profile.email || t('account.noEmail')}</dd>
      </dl>

      <form onSubmit={submit} noValidate className="border-t-2 border-line px-5 py-5 max-w-md">
        <h3 className="text-sm font-bold mb-1">
          {profile.has_password ? t('account.passwordTitle') : t('account.setPasswordTitle')}
        </h3>
        <p className="text-soft text-sm leading-relaxed mb-4">
          {profile.has_password ? t('account.passwordDesc') : t('account.setPasswordDesc')}
        </p>
        {/* Lets the browser's password manager tie the new password to this account. */}
        <input type="text" name="username" autoComplete="username" value={profile.username} readOnly hidden />
        {profile.has_password ? (
          <Field
            id="current-password"
            label={t('account.currentPassword')}
            type="password"
            autoComplete="current-password"
            required
            value={current}
            onChange={(e) => setCurrent(e.target.value)}
          />
        ) : null}
        <Field
          id="new-password"
          label={t('account.newPassword')}
          hint={t('account.newPasswordHint')}
          type="password"
          autoComplete="new-password"
          required
          value={next}
          onChange={(e) => setNext(e.target.value)}
        />
        <Field
          id="confirm-password"
          label={t('account.confirmPassword')}
          type="password"
          autoComplete="new-password"
          required
          value={confirm}
          onChange={(e) => setConfirm(e.target.value)}
        />
        {error ? (
          <p role="alert" className="text-sm mb-4 text-bad">
            {error}
          </p>
        ) : null}
        <button type="submit" disabled={busy} className="btn-ink text-sm px-4 py-2 disabled:opacity-60">
          {busy ? t('account.submitting') : t('account.submit')}
        </button>
      </form>
    </Panel>
  )
}

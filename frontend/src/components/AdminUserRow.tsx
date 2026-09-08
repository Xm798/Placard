import { useTranslation } from 'react-i18next'
import { fmtDateTime } from '../lib/format'

export interface AdminUser {
  id: string
  username: string
  email?: string
  display_name: string
  is_admin: boolean
  disabled: boolean
  has_password: boolean
  create_time: string
  last_login_at: string | null
  last_active_at: string | null
}

interface AdminUserRowProps {
  user: AdminUser
  isLast: boolean
  // isSelf greys out the actions the server refuses on the calling admin's own
  // account, so the page says no before the request does.
  isSelf: boolean
  // busy is true while ANY row has a request in flight: the page runs one
  // account action at a time, and a button that still looked clickable would
  // swallow the click after the operator confirmed it.
  busy: boolean
  onToggleDisabled: (user: AdminUser) => void
  onToggleAdmin: (user: AdminUser) => void
  onResetPassword: (user: AdminUser) => void
  onDelete: (user: AdminUser) => void
}

export default function AdminUserRow({
  user,
  isLast,
  isSelf,
  busy,
  onToggleDisabled,
  onToggleAdmin,
  onResetPassword,
  onDelete,
}: AdminUserRowProps) {
  const { t } = useTranslation()
  const selfTitle = isSelf ? t('adminUserRow.notSelfTitle') : undefined
  return (
    <div className={'px-5 py-4 flex items-start justify-between gap-4 flex-wrap' + (isLast ? '' : ' border-b border-line')}>
      <div className="min-w-0">
        <div className="flex items-center gap-2 flex-wrap">
          <span className="font-medium text-sm">{user.display_name || user.username}</span>
          <span className="mono text-xs text-faint">@{user.username}</span>
          {user.is_admin && (
            <span className="text-xs px-2 py-0.5 rounded-md border border-line-strong">
              {t('adminUserRow.adminBadge')}
            </span>
          )}
          {user.disabled && (
            <span className="text-xs px-2 py-0.5 rounded-md border border-line-strong text-soft">
              {t('adminUserRow.disabledBadge')}
            </span>
          )}
        </div>
        <div className="text-faint text-xs mt-1 flex items-center gap-3 flex-wrap">
          <span>{user.email || t('adminUserRow.noEmail')}</span>
          <span>{t('adminUserRow.registered', { time: fmtDateTime(user.create_time) })}</span>
          <span>
            {user.last_login_at
              ? t('adminUserRow.lastLogin', { time: fmtDateTime(user.last_login_at) })
              : t('adminUserRow.neverLoggedIn')}
          </span>
          {!user.has_password && <span>{t('adminUserRow.ssoOnly')}</span>}
        </div>
      </div>

      <div className="flex items-center gap-2 shrink-0">
        <button
          onClick={() => onToggleDisabled(user)}
          disabled={busy || isSelf}
          title={selfTitle}
          className="text-sm px-3 py-1.5 rounded-lg border border-line-strong disabled:opacity-40 disabled:cursor-not-allowed"
        >
          {user.disabled ? t('adminUserRow.enable') : t('adminUserRow.disable')}
        </button>
        <button
          onClick={() => onToggleAdmin(user)}
          disabled={busy || isSelf}
          title={selfTitle}
          className="text-sm px-3 py-1.5 rounded-lg border border-line-strong disabled:opacity-40 disabled:cursor-not-allowed"
        >
          {user.is_admin ? t('adminUserRow.demote') : t('adminUserRow.promote')}
        </button>
        <button
          onClick={() => onResetPassword(user)}
          disabled={busy}
          className="text-sm px-3 py-1.5 rounded-lg border border-line-strong disabled:opacity-40 disabled:cursor-not-allowed"
        >
          {t('adminUserRow.resetPassword')}
        </button>
        <button
          onClick={() => onDelete(user)}
          disabled={busy || isSelf}
          title={selfTitle}
          className="text-sm px-3 py-1.5 rounded-lg border border-line-strong disabled:opacity-40 disabled:cursor-not-allowed"
        >
          {t('common.delete')}
        </button>
      </div>
    </div>
  )
}

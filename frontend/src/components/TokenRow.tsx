// One API Token row in the settings list. Ported 1:1 from internal/web/app.js:344-362.

import { useTranslation } from 'react-i18next'
import { fmtDateTime, fmtExpiry } from '../lib/format'

export interface TokenItem {
  id: string | number
  name: string
  create_time: string
  last_used_at: string | null
  expires_at: string | null
  revoked: boolean
}

interface TokenRowProps {
  token: TokenItem
  isLast: boolean
  onRevoke: (id: string) => void
  onDelete: (id: string) => void
}

export default function TokenRow({ token: t, isLast, onRevoke, onDelete }: TokenRowProps) {
  const { t: tr } = useTranslation()
  // Meta string shape EXACT to app.js:346-348.
  const last =
    t.last_used_at == null
      ? tr('tokenRow.neverUsed')
      : tr('tokenRow.lastUsed', { time: fmtDateTime(t.last_used_at) })
  const exp = t.expires_at == null ? tr('tokenRow.longLived') : fmtExpiry(t.expires_at, tr)
  const meta =
    tr('tokenRow.created', { time: fmtDateTime(t.create_time) }) +
    ' · ' +
    last +
    ' · ' +
    exp +
    (t.revoked ? tr('tokenRow.revokedSuffix') : '')

  return (
    <div className={'row flex items-center gap-4 px-5 py-4' + (isLast ? '' : ' border-b border-line')}>
      <div className="flex-1 min-w-0">
        <div className="font-display font-semibold text-[15px] truncate">
          {t.name || tr('tokenRow.unnamed')}
        </div>
        <div className="text-xs text-faint mt-0.5">{meta}</div>
      </div>
      {t.revoked ? (
        <button
          className="btn-danger rounded-lg text-xs font-medium px-3 py-1.5 shrink-0"
          style={{ color: '#dc2626' }}
          aria-label={tr('tokenRow.deleteAria', { name: t.name || t.id })}
          onClick={() => onDelete(String(t.id))}
        >
          {tr('common.delete')}
        </button>
      ) : (
        <button
          className="btn-danger rounded-lg text-xs font-medium px-3 py-1.5 shrink-0"
          style={{ color: '#dc2626' }}
          aria-label={tr('tokenRow.revokeAria', { name: t.name || t.id })}
          onClick={() => onRevoke(String(t.id))}
        >
          {tr('tokenRow.revoke')}
        </button>
      )}
    </div>
  )
}

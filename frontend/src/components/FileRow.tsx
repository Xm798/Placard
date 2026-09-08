// One file row in the "my files" list (ported from internal/web/app.js:255-292),
// extended with the versioning entries: update-in-place and the expandable
// version history panel.

import { useTranslation } from 'react-i18next'
import { fmtDateTime, fmtExpiry, stripScheme } from '../lib/format'
import { visibilityLabel } from '../lib/visibility'
import VersionPanel, { type VersionsData } from './VersionPanel'

export interface FileItem {
  id: string
  title: string
  url: string
  create_time: string
  expires_at: string | null
  view_count: number
  latest_version: number
  shared_version: number
  visibility: string
  // Present only when the page has a share code; owner-scoped list only.
  share_code?: string
}

// Inline eye SVG (app.js:232 EYE_SVG).
function EyeIcon() {
  return (
    <svg
      className="w-3.5 h-3.5"
      aria-hidden="true"
      fill="none"
      viewBox="0 0 24 24"
      stroke="currentColor"
      strokeWidth="2"
    >
      <path d="M2.04 12.32a1 1 0 010-.64C3.42 7.5 7.36 4.5 12 4.5s8.58 3 9.96 7.18a1 1 0 010 .64C20.58 16.5 16.64 19.5 12 19.5s-8.58-3-9.96-7.18z" />
      <circle cx="12" cy="12" r="3" />
    </svg>
  )
}

interface FileRowProps {
  file: FileItem
  isLast: boolean
  expanded: boolean
  versions: VersionsData | null
  busy: boolean // a pin/restore request is in flight for this row
  onCopy: (id: string) => void
  onDelete: (id: string) => void
  onUpdate: (id: string) => void
  onToggleVersions: (id: string) => void
  onPin: (id: string, v: number) => void
  onRestore: (id: string, v: number) => void
  onShare: (id: string) => void
}

export default function FileRow({
  file,
  isLast,
  expanded,
  versions,
  busy,
  onCopy,
  onDelete,
  onUpdate,
  onToggleVersions,
  onPin,
  onRestore,
  onShare,
}: FileRowProps) {
  const { t } = useTranslation()
  const f = file
  const display = stripScheme(f.url || '')
  const label = f.title || f.id
  // A value outside the current vocabulary (a page published before the set
  // narrowed) shows its raw value rather than an empty badge — the owner can
  // then pick a current one from the share dialog.
  const visLabel = visibilityLabel(f.visibility, t)

  return (
    <div className={isLast ? '' : 'border-b border-line'}>
      <div className="row flex items-center gap-4 px-5 py-4">
        <a
          className="flex-1 min-w-0 block"
          href={'/s/' + encodeURIComponent(f.id)}
          aria-label={t('fileRow.open', { name: label })}
          style={{ cursor: 'pointer' }}
        >
          <div className="font-display font-semibold text-[15px] truncate ul">
            {f.title || t('fileRow.untitled')}
          </div>
          <div className="text-xs text-faint mono mt-0.5 truncate">{display}</div>
        </a>

        <div className="hidden sm:block text-xs text-faint w-24 text-right">{fmtDateTime(f.create_time)}</div>
        <div className="hidden md:block text-xs text-soft w-28 text-right">{fmtExpiry(f.expires_at, t)}</div>
        {visLabel && (
          <div
            className="hidden md:block text-xs text-soft w-14 text-right shrink-0"
            title={t('fileRow.visibilityTitle', { label: visLabel })}
          >
            {visLabel}
          </div>
        )}

        <div
          className="hidden sm:flex items-center gap-1 text-xs text-soft w-16 justify-end"
          title={t('fileRow.viewsTitle')}
          aria-label={t('fileRow.viewsAria', { count: f.view_count })}
        >
          <EyeIcon />
          {' ' + f.view_count}
        </div>

        <div className="flex gap-1 shrink-0">
          <button
            className="btn-outline rounded-lg text-xs font-medium px-3 py-1.5"
            aria-label={t('fileRow.updateAria', { name: label })}
            onClick={() => onUpdate(f.id)}
          >
            {t('fileRow.update')}
          </button>
          <button
            className="btn-outline rounded-lg text-xs font-medium px-3 py-1.5"
            aria-label={t('fileRow.versionsAria', { name: label })}
            aria-expanded={expanded}
            onClick={() => onToggleVersions(f.id)}
          >
            v{f.latest_version}
            {f.shared_version > 0 ? t('fileRow.pinnedBadge', { version: f.shared_version }) : ''}
          </button>
          <button
            className="btn-outline rounded-lg text-xs font-medium px-3 py-1.5"
            aria-label={t('fileRow.copyAria', { name: label })}
            onClick={() => onCopy(f.id)}
          >
            {t('common.copy')}
          </button>
          <button
            className="btn-outline rounded-lg text-xs font-medium px-3 py-1.5"
            aria-label={t('fileRow.shareAria', { name: label })}
            onClick={() => onShare(f.id)}
          >
            {t('fileRow.share')}
          </button>
          <button
            className="btn-danger rounded-lg text-xs font-medium px-3 py-1.5"
            aria-label={t('fileRow.deleteAria', { name: label })}
            style={{ color: '#dc2626' }}
            onClick={() => onDelete(f.id)}
          >
            {t('common.delete')}
          </button>
        </div>
      </div>
      {expanded && versions && (
        <VersionPanel
          data={versions}
          busy={busy}
          onPin={(v) => onPin(f.id, v)}
          onRestore={(v) => onRestore(f.id, v)}
        />
      )}
    </div>
  )
}

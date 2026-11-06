// One file tile in the "my files" grid, including the versioning entries:
// update-in-place and the expandable version history panel.

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
    <article className="card flex flex-col">
      <div className="flex items-center justify-between gap-3 px-5 pt-4">
        {visLabel ? (
          <span
            className="sticker text-[11px] uppercase tracking-wide px-2 py-0.5"
            title={t('fileRow.visibilityTitle', { label: visLabel })}
          >
            {visLabel}
          </span>
        ) : (
          <span />
        )}
        <span
          className="flex items-center gap-1 text-xs font-bold"
          title={t('fileRow.viewsTitle')}
          aria-label={t('fileRow.viewsAria', { count: f.view_count })}
        >
          <EyeIcon />
          {' ' + f.view_count}
        </span>
      </div>

      <a
        className="block px-5 pt-3 pb-4 min-w-0"
        href={'/s/' + encodeURIComponent(f.id)}
        aria-label={t('fileRow.open', { name: label })}
      >
        <div className="font-display text-xl leading-tight truncate">
          <span className="ul">{f.title || t('fileRow.untitled')}</span>
        </div>
        <div className="text-xs text-faint mono mt-1.5 truncate">{display}</div>
      </a>

      <dl className="tile-meta grid grid-cols-2 text-xs mt-auto">
        <div className="px-5 py-2.5">
          <dt className="text-faint">{t('fileRow.createdLabel')}</dt>
          <dd className="font-bold mt-0.5">{fmtDateTime(f.create_time)}</dd>
        </div>
        <div className="px-5 py-2.5">
          <dt className="text-faint">{t('fileRow.expiresLabel')}</dt>
          <dd className="font-bold mt-0.5">{fmtExpiry(f.expires_at, t)}</dd>
        </div>
      </dl>

      <div className="tile-actions flex flex-wrap gap-2 px-5 py-4">
        <button
          className="btn-ink text-xs px-3 py-1.5"
          aria-label={t('fileRow.updateAria', { name: label })}
          onClick={() => onUpdate(f.id)}
        >
          {t('fileRow.update')}
        </button>
        <button
          className="btn-outline text-xs px-3 py-1.5"
          aria-label={t('fileRow.versionsAria', { name: label })}
          aria-expanded={expanded}
          onClick={() => onToggleVersions(f.id)}
        >
          v{f.latest_version}
          {f.shared_version > 0 ? t('fileRow.pinnedBadge', { version: f.shared_version }) : ''}
        </button>
        <button
          className="btn-outline text-xs px-3 py-1.5"
          aria-label={t('fileRow.copyAria', { name: label })}
          onClick={() => onCopy(f.id)}
        >
          {t('common.copy')}
        </button>
        <button
          className="btn-outline text-xs px-3 py-1.5"
          aria-label={t('fileRow.shareAria', { name: label })}
          onClick={() => onShare(f.id)}
        >
          {t('fileRow.share')}
        </button>
        <button
          className="btn-danger text-xs px-3 py-1.5 ml-auto"
          aria-label={t('fileRow.deleteAria', { name: label })}
          onClick={() => onDelete(f.id)}
        >
          {t('common.delete')}
        </button>
      </div>
      {expanded && versions && (
        <VersionPanel
          data={versions}
          busy={busy}
          onPin={(v) => onPin(f.id, v)}
          onRestore={(v) => onRestore(f.id, v)}
        />
      )}
    </article>
  )
}

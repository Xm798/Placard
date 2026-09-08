// Version history panel under a file row: shared-version selector (follow
// latest, or pin a version) plus the version list with per-version restore.

import { useTranslation } from 'react-i18next'
import { fmtDateTime } from '../lib/format'

export interface VersionInfo {
  version: number
  title: string
  size_bytes: number
  create_time: string
}

export interface VersionsData {
  latest_version: number
  shared_version: number // 0 = follow latest
  versions: VersionInfo[]
}

interface VersionPanelProps {
  data: VersionsData
  busy?: boolean // pin/restore request in flight → lock the panel against re-entrant clicks
  onPin: (v: number) => void // 0 = follow latest
  onRestore: (v: number) => void
}

export default function VersionPanel({ data, busy = false, onPin, onRestore }: VersionPanelProps) {
  const { t } = useTranslation()
  return (
    <div className="px-5 py-3 border-t border-line" style={{ background: 'var(--bg)' }}>
      <div className="flex items-center gap-2 mb-2 text-xs">
        <span className="text-faint">{t('versions.sharedVersion')}</span>
        <select
          className="border border-line-strong rounded-lg px-2 py-1 bg-transparent"
          aria-label={t('versions.sharedVersion')}
          value={String(data.shared_version)}
          disabled={busy}
          onChange={(e) => onPin(Number(e.target.value))}
        >
          <option value="0">{t('versions.followLatest', { version: data.latest_version })}</option>
          {data.versions.map((v) => (
            <option key={v.version} value={String(v.version)}>
              {t('versions.pinTo', { version: v.version })}
            </option>
          ))}
        </select>
      </div>
      <div>
        {data.versions.map((v) => (
          <div key={v.version} className="flex items-center gap-3 py-1.5 text-xs">
            <span className="mono w-10 shrink-0">v{v.version}</span>
            <span className="flex-1 min-w-0 truncate">{v.title || t('versions.untitled')}</span>
            <span className="text-faint w-28 text-right shrink-0">{fmtDateTime(v.create_time)}</span>
            {v.version === data.latest_version ? (
              <span className="text-faint w-16 text-right shrink-0">{t('versions.latest')}</span>
            ) : (
              <button
                className="btn-outline rounded-lg px-2 py-1 w-16 shrink-0"
                aria-label={t('versions.restoreAria', { version: v.version })}
                disabled={busy}
                onClick={() => onRestore(v.version)}
              >
                {t('versions.restore')}
              </button>
            )}
          </div>
        ))}
      </div>
    </div>
  )
}

import { useRef } from 'react'
import { Trans, useTranslation } from 'react-i18next'

// Dropzone. Ported 1:1 from internal/web/app.html:228-238 + drag/drop wiring
// app.js:165-171. Click opens the hidden file input; drag adds .dragover; drop
// hands the first file to onFile. The inner "choose file" button stops
// propagation (app.js:486) so it doesn't double-fire the dropzone click.

interface DropzoneProps {
  onFile: (f: File) => void
}

export default function Dropzone({ onFile }: DropzoneProps) {
  const { t } = useTranslation()
  const dz = useRef<HTMLDivElement>(null)
  const fi = useRef<HTMLInputElement>(null)

  return (
    <div
      ref={dz}
      className="dropzone rounded-2xl px-8 py-14 sm:py-16 text-center cursor-pointer"
      id="dropzone"
      onClick={() => fi.current?.click()}
      onDragOver={(e) => {
        e.preventDefault()
        dz.current?.classList.add('dragover')
      }}
      onDragLeave={(e) => {
        e.preventDefault()
        dz.current?.classList.remove('dragover')
      }}
      onDrop={(e) => {
        e.preventDefault()
        dz.current?.classList.remove('dragover')
        if (e.dataTransfer.files.length) onFile(e.dataTransfer.files[0])
      }}
    >
      <div className="mx-auto w-14 h-14 rounded-xl surface border border-line-strong flex items-center justify-center mb-5">
        <svg
          className="w-6 h-6"
          style={{ color: 'var(--fg-soft)' }}
          fill="none"
          viewBox="0 0 24 24"
          stroke="currentColor"
          strokeWidth="1.5"
        >
          <path d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1M16 7l-4-4m0 0L8 7m4-4v12" />
        </svg>
      </div>
      <p className="font-display font-semibold text-xl mb-1.5">{t('dropzone.title')}</p>
      <p className="text-soft text-sm mb-6">
        <Trans
          i18nKey="dropzone.hint"
          components={{ pick: <span className="font-medium ul" style={{ color: 'var(--fg)' }} /> }}
        />
      </p>
      <button
        className="btn-ink font-medium text-sm rounded-lg px-6 py-2.5"
        data-action="pick-file"
        onClick={(e) => {
          e.stopPropagation()
          fi.current?.click()
        }}
      >
        {t('dropzone.choose')}
      </button>
      <input
        ref={fi}
        type="file"
        id="file-input"
        accept=".html,.htm"
        className="hidden"
        onChange={(e) => {
          const f = e.target.files
          if (f && f.length) onFile(f[0])
        }}
      />
    </div>
  )
}

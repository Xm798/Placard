import { useRef, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

// Dropzone. Click opens the hidden file input; drag adds .dragover; drop
// hands the first file to onFile. The inner "choose file" button stops
// propagation (app.js:486) so it doesn't double-fire the dropzone click.

interface DropzoneProps {
  onFile: (f: File) => void
  // The page's own headline, set inside the drop target so the poster is the
  // whole call to action.
  heading: ReactNode
  note?: ReactNode
}

export default function Dropzone({ onFile, heading, note }: DropzoneProps) {
  const { t } = useTranslation()
  const dz = useRef<HTMLDivElement>(null)
  const fi = useRef<HTMLInputElement>(null)

  return (
    <div
      ref={dz}
      className="dropzone relative min-h-[460px] p-7 sm:p-10 flex flex-col justify-between gap-10 cursor-pointer"
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
      <div className="dropzone-icon absolute top-6 right-6 sm:top-8 sm:right-8 w-16 h-16 rotate-6 flex items-center justify-center" aria-hidden="true">
        <svg className="w-7 h-7" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth="2.5">
          <path d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1M16 7l-4-4m0 0L8 7m4-4v12" />
        </svg>
      </div>
      <div className="pr-20">
        <h1 className="font-display uppercase leading-[0.92] text-5xl sm:text-7xl max-w-[12ch]">{heading}</h1>
        <p className="font-display text-xl sm:text-2xl mt-4">{t('dropzone.title')}</p>
        {note ? <div className="mt-6 max-w-xl text-sm leading-relaxed">{note}</div> : null}
      </div>
      <div className="flex flex-col sm:flex-row sm:items-center gap-4">
        <button
          className="btn-ink text-base uppercase tracking-wide px-7 py-3 self-start"
          data-action="pick-file"
          onClick={(e) => {
            e.stopPropagation()
            fi.current?.click()
          }}
        >
          {t('dropzone.choose')}
        </button>
        <p className="text-sm font-bold">{t('dropzone.hint')}</p>
      </div>
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

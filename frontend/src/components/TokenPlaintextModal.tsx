// One-shot token plaintext dialog (focus trap). Ported from
// internal/web/app.html:432-446 + app.js:416-449. Uses shadcn Dialog (Radix)
// for the focus trap / Esc / inert backdrop. Content is rendered ONLY while
// token != null, so on close the whole subtree unmounts and the plaintext is
// gone from the DOM (app.js:444-446).

import { useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { Dialog, DialogContent, DialogTitle } from './ui/dialog'
import { useCopy } from '../hooks/useCopy'

interface TokenPlaintextModalProps {
  token: string | null
  onClose: () => void
}

export default function TokenPlaintextModal({ token, onClose }: TokenPlaintextModalProps) {
  const copy = useCopy()
  const { t } = useTranslation()
  const copyRef = useRef<HTMLButtonElement>(null)

  function copyToken() {
    if (token == null) return
    copy(token, t('tokenPlaintext.copied'))
  }

  return (
    <Dialog
      open={token != null}
      onOpenChange={(o) => {
        if (!o) onClose()
      }}
    >
      {token != null && (
        <DialogContent
          showCloseButton={false}
          aria-describedby={undefined}
          onOpenAutoFocus={(e) => {
            // Focus the copy button on open (app.js:437).
            e.preventDefault()
            copyRef.current?.focus()
          }}
          // Once-shown plaintext must not be lost to a stray backdrop click
          // (app.js: only the close button or Esc close it). Esc stays enabled.
          onInteractOutside={(e) => e.preventDefault()}
          onPointerDownOutside={(e) => e.preventDefault()}
          className="surface sm:max-w-lg w-full rounded-2xl border border-line-strong p-6 gap-0"
        >
          <DialogTitle className="font-display font-bold text-xl mb-1">
            {t('tokenPlaintext.title')}
          </DialogTitle>
          <p className="text-sm mb-5" style={{ color: '#dc2626' }}>
            {t('tokenPlaintext.warning')}
          </p>
          <div
            className="flex items-center gap-2 p-2 pl-4 rounded-xl border border-line-strong mb-3"
            style={{ background: 'var(--bg)' }}
          >
            <code className="flex-1 mono text-sm truncate">{token}</code>
            <button
              ref={copyRef}
              type="button"
              onClick={copyToken}
              aria-label={t('tokenPlaintext.copyAria')}
              className="btn-ink shrink-0 rounded-lg text-sm font-medium px-4 py-2"
            >
              {t('common.copy')}
            </button>
          </div>
          <p className="text-faint text-xs mono mb-5">{'export PLACARD_TOKEN=' + token}</p>
          <div className="flex justify-end">
            <button
              type="button"
              onClick={onClose}
              className="btn-outline rounded-lg text-sm font-medium px-4 py-2"
            >
              {t('tokenPlaintext.close')}
            </button>
          </div>
        </DialogContent>
      )}
    </Dialog>
  )
}

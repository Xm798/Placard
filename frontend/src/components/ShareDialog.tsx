// Share settings dialog: visibility picker (PATCHes on click) plus access-code
// generate / copy / clear. The dialog structure mirrors TokenCreateModal; the
// tiers reuse ExpirySegmented.

import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Dialog, DialogContent, DialogTitle } from './ui/dialog'
import ExpirySegmented from './ExpirySegmented'
import { api } from '../lib/api'
import { visibilityHint, visibilityOptions } from '../lib/visibility'
import { useCopy } from '../hooks/useCopy'
import { useToast } from '../hooks/useToast'

export interface ShareDialogFile {
  id: string
  title: string
  visibility: string
  // The plaintext share code, absent when the page has none. It reaches the
  // browser only through the owner's own /api/files list.
  share_code?: string
}

interface ShareDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  file: ShareDialogFile | null
  onVisibilityChange: (id: string, visibility: string) => void
  onShareCodeChange: (id: string, shareCode: string) => void
}

export default function ShareDialog({
  open,
  onOpenChange,
  file,
  onVisibilityChange,
  onShareCodeChange,
}: ShareDialogProps) {
  const { show } = useToast()
  const { t } = useTranslation()
  const copy = useCopy()
  const [visibility, setVisibility] = useState('link')
  const [shareCode, setShareCode] = useState('')
  const [codeBusy, setCodeBusy] = useState(false)
  const fileId = file?.id
  const fileVisibility = file?.visibility
  const fileShareCode = file?.share_code

  // Per-open reset: seed both controls from the file being shared.
  useEffect(() => {
    if (!open || !fileId) return
    setVisibility(fileVisibility ?? 'link')
    setShareCode(fileShareCode ?? '')
    setCodeBusy(false)
  }, [open, fileId, fileVisibility, fileShareCode])

  if (!file) return null

  function changeVisibility(v: string) {
    const prev = visibility
    setVisibility(v)
    api('/api/files/' + encodeURIComponent(file!.id), {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ visibility: v }),
    })
      .then(() => {
        show(t('share.visibilityUpdated'))
        onVisibilityChange(file!.id, v)
      })
      .catch(() => {
        setVisibility(prev)
        show(t('share.updateFailed'))
      })
  }

  // Generating replaces any existing code, which is also how an owner revokes
  // access: the server invalidates every visitor already let in.
  function generateCode() {
    setCodeBusy(true)
    api('/api/files/' + encodeURIComponent(file!.id) + '/share-code', { method: 'POST' })
      .then((r) => r.json())
      .then((d) => {
        setShareCode(d.share_code)
        onShareCodeChange(file!.id, d.share_code)
        show(shareCode ? t('share.codeUpdated') : t('share.codeGenerated'))
      })
      .catch(() => show(t('share.generateFailed')))
      .finally(() => setCodeBusy(false))
  }

  function clearCode() {
    setCodeBusy(true)
    api('/api/files/' + encodeURIComponent(file!.id) + '/share-code', { method: 'DELETE' })
      .then(() => {
        setShareCode('')
        onShareCodeChange(file!.id, '')
        show(t('share.codeCleared'))
      })
      .catch(() => show(t('share.clearFailed')))
      .finally(() => setCodeBusy(false))
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        showCloseButton={false}
        aria-describedby={undefined}
        className="surface sm:max-w-lg w-full rounded-2xl border border-line-strong p-6 gap-0"
      >
        <DialogTitle className="font-display font-bold text-xl mb-1">{t('share.title')}</DialogTitle>
        <p className="text-faint text-xs mb-4 truncate">{file.title || file.id}</p>

        <div className="mb-4">
          <span className="text-faint text-sm block mb-1.5" id="share-visibility-label">
            {t('share.visibility')}
          </span>
          <ExpirySegmented
            options={visibilityOptions(t)}
            value={visibility}
            onChange={changeVisibility}
            ariaLabelledBy="share-visibility-label"
            btnClassName="share-visibility-btn px-3 py-1.5 font-medium text-sm"
          />
          <p className="text-faint text-xs mt-2 leading-relaxed">{visibilityHint(visibility, t)}</p>
        </div>

        <div className="mb-4">
          <span className="text-faint text-sm block mb-1.5" id="share-code-label">
            {t('share.code')}
          </span>
          <div className="flex items-center gap-2" aria-labelledby="share-code-label">
            <span className="font-mono text-lg tracking-[0.3em] tabular-nums" data-testid="share-code">
              {shareCode || t('share.codePlaceholder')}
            </span>
            {shareCode && (
              <button
                type="button"
                onClick={() => copy(shareCode, t('share.codeCopied'))}
                className="btn-outline rounded-lg text-xs font-medium px-2.5 py-1"
              >
                {t('common.copy')}
              </button>
            )}
            <button
              type="button"
              onClick={generateCode}
              disabled={codeBusy}
              className="btn-outline rounded-lg text-xs font-medium px-2.5 py-1 disabled:opacity-55"
            >
              {shareCode ? t('share.regenerate') : t('common.generate')}
            </button>
            {shareCode && (
              <button
                type="button"
                onClick={clearCode}
                disabled={codeBusy}
                className="btn-outline rounded-lg text-xs font-medium px-2.5 py-1 disabled:opacity-55"
              >
                {t('share.clear')}
              </button>
            )}
          </div>
          <p className="text-faint text-xs mt-2 leading-relaxed">
            {shareCode ? t('share.codeHintSet') : t('share.codeHintUnset')}
          </p>
        </div>

        <div className="flex justify-end gap-2 mt-4">
          <button
            type="button"
            onClick={() => onOpenChange(false)}
            className="btn-outline rounded-lg text-sm font-medium px-4 py-2"
          >
            {t('share.done')}
          </button>
        </div>
      </DialogContent>
    </Dialog>
  )
}

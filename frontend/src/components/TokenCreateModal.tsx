// Token creation dialog (name + lifetime). Ported from
// internal/web/app.html:409-430 + app.js:377-414. Uses shadcn Dialog (Radix)
// for the focus trap / Esc / inert backdrop; the interior keeps app.html's
// exact classes for fidelity.

import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Dialog, DialogContent, DialogTitle } from './ui/dialog'
import ExpirySegmented from './ExpirySegmented'
import { api } from '../lib/api'
import { useToast } from '../hooks/useToast'

const EXPIRY_VALUES = ['30d', '90d', '365d']
const DEFAULT_EXPIRY = '90d'

interface TokenCreateModalProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  onCreated: (plaintext: string) => void
}

export default function TokenCreateModal({ open, onOpenChange, onCreated }: TokenCreateModalProps) {
  const { show } = useToast()
  const { t } = useTranslation()
  const [name, setName] = useState('')
  const [expiry, setExpiry] = useState(DEFAULT_EXPIRY)
  const nameRef = useRef<HTMLInputElement>(null)

  // Reset name + expiry to defaults each open (app.js:391-393).
  useEffect(() => {
    if (open) {
      setName('')
      setExpiry(DEFAULT_EXPIRY)
    }
  }, [open])

  function submit() {
    api('/api/tokens', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name: name.trim(), expiry }),
    })
      .then((r) => r.json())
      .then((data) => {
        onOpenChange(false)
        onCreated(data.token)
      })
      .catch(() => show(t('tokenCreate.failed')))
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        showCloseButton={false}
        aria-describedby={undefined}
        onOpenAutoFocus={(e) => {
          // Autofocus the name input (app.js:395), not Radix's default target.
          e.preventDefault()
          nameRef.current?.focus()
        }}
        className="surface sm:max-w-lg w-full rounded-2xl border border-line-strong p-6 gap-0"
      >
        <DialogTitle className="font-display font-bold text-xl mb-4">{t('tokenCreate.title')}</DialogTitle>
        <div className="mb-4">
          <label className="text-faint text-sm block mb-1.5" htmlFor="token-name">
            {t('tokenCreate.name')}
          </label>
          <input
            ref={nameRef}
            type="text"
            id="token-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            className="w-full bg-transparent border border-line-strong rounded-lg focus:border-[var(--fg)] outline-none px-3 py-2 transition-colors"
            placeholder={t('tokenCreate.namePlaceholder')}
          />
        </div>
        <div className="mb-5">
          <span className="text-faint text-sm block mb-1.5" id="token-expiry-label">
            {t('tokenCreate.expiry')}
          </span>
          <ExpirySegmented
            options={EXPIRY_VALUES.map((val) => ({ val, label: t('tokenCreate.expiryOptions.' + val) }))}
            value={expiry}
            onChange={setExpiry}
            ariaLabelledBy="token-expiry-label"
            btnClassName="token-expiry-btn px-3 py-1.5 font-medium text-sm"
          />
        </div>
        <div className="flex justify-end gap-2">
          <button
            type="button"
            onClick={() => onOpenChange(false)}
            className="btn-outline rounded-lg text-sm font-medium px-4 py-2"
          >
            {t('common.cancel')}
          </button>
          <button
            type="button"
            onClick={submit}
            className="btn-ink rounded-lg text-sm font-medium px-4 py-2"
          >
            {t('common.generate')}
          </button>
        </div>
      </DialogContent>
    </Dialog>
  )
}

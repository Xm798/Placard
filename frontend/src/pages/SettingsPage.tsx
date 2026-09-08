// Settings (token management) page. Ported 1:1 from
// internal/web/app.html:336-356 + app.js:330-343 (loadTokens / revokeToken).
// The `.page` class is dropped: it is display:none by default (SPA toggled it),
// whereas React Router mounts one page.

import { useCallback, useEffect, useState } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import i18n from '../i18n'
import { api, type HttpError } from '../lib/api'
import { useToast } from '../hooks/useToast'
import TokenRow, { type TokenItem } from '../components/TokenRow'
import TokenCreateModal from '../components/TokenCreateModal'
import TokenPlaintextModal from '../components/TokenPlaintextModal'
import ExpirySegmented from '../components/ExpirySegmented'
import IdentitySection from '../components/IdentitySection'
import { visibilityOptions } from '../lib/visibility'

export default function SettingsPage() {
  const { show } = useToast()
  const { t } = useTranslation()
  const [tokens, setTokens] = useState<TokenItem[]>([])
  const [createOpen, setCreateOpen] = useState(false)
  const [plaintext, setPlaintext] = useState<string | null>(null)
  const [defaultVisibility, setDefaultVisibility] = useState('link')

  // A failure toast is written at the moment it is shown, so it reads the active
  // language off the i18n instance rather than through the hook's `t`. That keeps
  // `t` — whose identity changes on every language switch — out of the loader's
  // dependencies, where it would refetch and reset the page under a reader who
  // only changed language.
  const loadTokens = useCallback(() => {
    api('/api/tokens')
      .then((r) => r.json())
      .then((data) => setTokens(data.tokens || []))
      .catch(() => show(i18n.t('settings.tokensLoadFailed')))
  }, [show])

  const loadPrefs = useCallback(() => {
    api('/api/prefs')
      .then((r) => r.json())
      .then((data: { default_visibility?: string }) => setDefaultVisibility(data.default_visibility || 'link'))
      .catch(() => show(i18n.t('settings.prefsLoadFailed')))
  }, [show])

  useEffect(() => {
    loadTokens()
  }, [loadTokens])

  useEffect(() => {
    loadPrefs()
  }, [loadPrefs])

  // Optimistic switch + PUT; 503 means the profile row isn't ready yet (no
  // login upsert has landed) — a distinct message from a generic failure so
  // the user knows re-logging in is the fix, not retrying.
  function changeDefaultVisibility(v: string) {
    const prev = defaultVisibility
    setDefaultVisibility(v)
    api('/api/prefs', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ default_visibility: v }),
    })
      .then(() => show(t('settings.defaultVisibilityUpdated')))
      .catch((e: HttpError) => {
        setDefaultVisibility(prev)
        show(e.status === 503 ? t('settings.profileNotReady') : t('settings.updateFailed'))
      })
  }

  function revokeToken(id: string) {
    if (!confirm(t('settings.revokeConfirm'))) return
    api('/api/tokens/' + encodeURIComponent(id), { method: 'DELETE' })
      .then(() => {
        show(t('settings.revoked'))
        loadTokens()
      })
      .catch(() => show(t('settings.revokeFailed')))
  }

  function deleteToken(id: string) {
    if (!confirm(t('settings.deleteConfirm'))) return
    api('/api/tokens/' + encodeURIComponent(id) + '/permanent', { method: 'DELETE' })
      .then(() => {
        show(t('settings.deleted'))
        loadTokens()
      })
      .catch(() => show(t('settings.deleteFailed')))
  }

  return (
    <section>
      <div className="pt-12 pb-8">
        <h1 className="font-display font-extrabold text-4xl sm:text-5xl tracking-[-0.02em] mb-2">
          {t('settings.title')}
        </h1>
        <p className="text-soft">{t('settings.sub')}</p>
      </div>

      <div className="rounded-2xl border border-line surface p-6 mb-6">
        <div className="flex items-start justify-between gap-4 flex-wrap">
          <div className="max-w-md">
            <h2 className="font-display font-bold text-lg mb-1">{t('settings.tokenCardTitle')}</h2>
            <p className="text-soft text-sm leading-relaxed">
              <Trans
                i18nKey="settings.tokenCardDesc"
                components={{ code: <code className="mono text-[0.85em]" /> }}
              />
            </p>
          </div>
          <button
            onClick={() => setCreateOpen(true)}
            className="btn-ink rounded-lg text-sm font-medium px-4 py-2.5 shrink-0"
          >
            {t('settings.createToken')}
          </button>
        </div>
      </div>

      <div className="rounded-2xl border border-line surface p-6 mb-6">
        <div className="max-w-md mb-3">
          <h2 className="font-display font-bold text-lg mb-1" id="default-visibility-label">
            {t('settings.defaultVisibilityTitle')}
          </h2>
          <p className="text-soft text-sm leading-relaxed">{t('settings.defaultVisibilityDesc')}</p>
        </div>
        <ExpirySegmented
          options={visibilityOptions(t)}
          value={defaultVisibility}
          onChange={changeDefaultVisibility}
          ariaLabelledBy="default-visibility-label"
        />
      </div>

      <IdentitySection />

      <div className="rounded-2xl border border-line overflow-hidden surface">
        {tokens.length === 0 ? (
          <div className="px-5 py-12 text-center text-soft text-sm">
            {t('settings.emptyTokens')}
          </div>
        ) : (
          tokens.map((token, i) => (
            <TokenRow
              key={String(token.id)}
              token={token}
              isLast={i === tokens.length - 1}
              onRevoke={revokeToken}
              onDelete={deleteToken}
            />
          ))
        )}
      </div>

      <TokenCreateModal
        open={createOpen}
        onOpenChange={setCreateOpen}
        onCreated={(pt) => {
          setPlaintext(pt)
          loadTokens()
        }}
      />
      <TokenPlaintextModal token={plaintext} onClose={() => setPlaintext(null)} />
    </section>
  )
}

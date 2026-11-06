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
import Panel from '../components/Panel'
import IdentitySection from '../components/IdentitySection'
import AccountSection from '../components/AccountSection'
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
    <section className="pt-12 sm:pt-16 grid gap-10 lg:grid-cols-[auto_minmax(0,1fr)] lg:gap-14 items-start">
      <div className="lg:sticky lg:top-12">
        <h1 className="font-display text-5xl sm:text-6xl leading-[0.95] lg:[writing-mode:vertical-rl] lg:tracking-[0.1em]">
          {t('settings.title')}
        </h1>
      </div>

      <div className="space-y-10 min-w-0">
        <AccountSection index={1} />

        <Panel
          index={2}
          title={t('settings.tokenCardTitle')}
          tone="pop"
          action={
            <button onClick={() => setCreateOpen(true)} className="btn-ink text-sm px-4 py-2 shrink-0">
              {t('settings.createToken')}
            </button>
          }
        >
          <p className="text-soft text-sm leading-relaxed px-5 py-4 max-w-2xl">
            <Trans i18nKey="settings.tokenCardDesc" components={{ code: <code className="mono text-[0.85em]" /> }} />
          </p>
          <div className="border-t-2 border-line">
            {tokens.length === 0 ? (
              <div className="px-5 py-10 text-center text-soft text-sm">{t('settings.emptyTokens')}</div>
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
        </Panel>

        <Panel index={3} title={t('settings.defaultVisibilityTitle')} titleId="default-visibility-label" tone="sun">
          <div className="px-5 py-5 flex flex-col sm:flex-row sm:items-center gap-4 sm:justify-between">
            <p className="text-soft text-sm leading-relaxed max-w-md">{t('settings.defaultVisibilityDesc')}</p>
            <ExpirySegmented
              options={visibilityOptions(t)}
              value={defaultVisibility}
              onChange={changeDefaultVisibility}
              ariaLabelledBy="default-visibility-label"
            />
          </div>
        </Panel>

        <IdentitySection index={4} />
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

import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import i18n from '../i18n'
import { api, type HttpError } from '../lib/api'
import { oidcStartURL } from '../lib/auth'
import { useToast } from '../hooks/useToast'

interface Identity {
  provider: string
  display_name: string
  linked_at: string
  can_unlink: boolean
}

interface Provider {
  name: string
  display_name: string
}

// The shape this component renders from. Both list fields are required here
// even though the server omits an empty `available`, because normalize() is
// what turns the wire body into this — a component must not decide what to
// render from fields a response may or may not carry.
interface Identities {
  identities: Identity[]
  has_password: boolean
  available: Provider[]
}

function normalize(body: Partial<Identities>): Identities {
  return {
    identities: body.identities ?? [],
    has_password: body.has_password ?? false,
    available: body.available ?? [],
  }
}

// IdentitySection manages the single sign-on providers bound to the account.
//
// Linking is a full page navigation rather than a fetch: it runs the same
// authorization flow the login page starts, and the browser has to leave for
// the provider and come back. Unlinking is an ordinary API call.
export default function IdentitySection() {
  const { show } = useToast()
  const { t } = useTranslation()
  const [data, setData] = useState<Identities | null>(null)

  // A failure toast is written at the moment it is shown, so it reads the active
  // language off the i18n instance rather than through the hook's `t`. That keeps
  // `t` — whose identity changes on every language switch — out of the loader's
  // dependencies, where it would refetch and reset the page under a reader who
  // only changed language.
  const load = useCallback(() => {
    api('/api/auth/identities')
      .then((r) => r.json())
      .then((body) => setData(normalize(body)))
      .catch(() => show(i18n.t('identity.loadFailed')))
  }, [show])

  useEffect(() => {
    load()
  }, [load])

  function unlink(provider: string, label: string) {
    if (!confirm(t('identity.unlinkConfirm', { name: label }))) return
    api('/api/auth/identities/' + encodeURIComponent(provider), { method: 'DELETE' })
      .then(() => {
        show(t('identity.unlinked', { name: label }))
        load()
      })
      .catch((e: HttpError) => {
        show(e.status === 400 ? t('identity.lastMethod') : t('identity.unlinkFailed'))
      })
  }

  // Nothing to manage on an instance with no providers configured and none
  // ever linked: the section would be an empty box explaining an absent
  // feature.
  if (!data || (data.identities.length === 0 && data.available.length === 0)) return null

  return (
    <div className="rounded-2xl border border-line surface p-6 mb-6">
      <div className="max-w-md mb-4">
        <h2 className="font-display font-bold text-lg mb-1">{t('identity.title')}</h2>
        <p className="text-soft text-sm leading-relaxed">
          {data.has_password ? t('identity.descWithPassword') : t('identity.descNoPassword')}
        </p>
      </div>

      <div className="flex flex-col gap-2">
        {data.identities.map((identity) => (
          <div
            key={identity.provider}
            className="flex items-center justify-between gap-4 rounded-lg border border-line px-4 py-3"
          >
            <div>
              <div className="text-sm font-medium">{identity.display_name}</div>
              <div className="text-faint text-xs mt-0.5">
                {t('identity.linkedAt', { date: new Date(identity.linked_at).toLocaleDateString() })}
              </div>
            </div>
            <button
              onClick={() => unlink(identity.provider, identity.display_name)}
              disabled={!identity.can_unlink}
              title={identity.can_unlink ? undefined : t('identity.lastMethodTitle')}
              className="text-sm px-3 py-1.5 rounded-lg border border-line-strong disabled:opacity-40 disabled:cursor-not-allowed"
            >
              {t('identity.unlink')}
            </button>
          </div>
        ))}

        {data.available.map((provider) => (
          <a
            key={provider.name}
            href={oidcStartURL(provider.name, '/settings')}
            className="flex items-center justify-between gap-4 rounded-lg border border-line border-dashed px-4 py-3 transition-colors hover:border-[var(--fg)]"
          >
            <span className="text-sm font-medium">{provider.display_name}</span>
            <span className="text-sm text-soft">{t('identity.link')}</span>
          </a>
        ))}
      </div>
    </div>
  )
}

import { useEffect, useState, type FormEvent } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import AuthShell, { Field } from './AuthShell'
import {
  fetchAuthStatus,
  login,
  oidcStartURL,
  safeRedirect,
  type AuthStatus,
  type OIDCProvider,
} from '../lib/auth'

// LoginFooter says what is actually known. "Registration is closed" is a claim
// about instance policy, so it is reserved for a status that was read
// successfully and said so — never for one that has not arrived or did not load.
function LoginFooter({ status, failed }: { status: AuthStatus | null; failed: boolean }) {
  if (failed) return <>Could not load sign-in options. Signing in still works.</>
  if (!status) return null
  if (!status.registration_open) {
    return <>Registration is closed on this instance. Ask an admin for an account.</>
  }
  return (
    <>
      No account yet?{' '}
      <Link to="/register" className="underline underline-offset-4" style={{ color: 'var(--fg)' }}>
        Create one
      </Link>
    </>
  )
}

// ProviderButtons renders one sign-in button per configured OIDC provider.
// They are plain links, not fetch() calls: the flow leaves this origin for the
// identity provider and returns as a browser redirect.
function ProviderButtons({ providers, redirect }: { providers: OIDCProvider[]; redirect: string }) {
  if (providers.length === 0) return null
  return (
    <>
      <div className="flex flex-col gap-2 mb-6">
        {providers.map((p) => (
          <a
            key={p.name}
            href={oidcStartURL(p.name, redirect)}
            className="w-full h-10 rounded-lg border border-line-strong flex items-center justify-center text-sm font-medium transition-colors hover:border-[var(--fg)]"
          >
            Continue with {p.display_name}
          </a>
        ))}
      </div>
      <div className="flex items-center gap-3 mb-6 text-faint text-xs">
        <span className="h-px flex-1" style={{ background: 'var(--line)' }} />
        or
        <span className="h-px flex-1" style={{ background: 'var(--line)' }} />
      </div>
    </>
  )
}

export default function LoginPage() {
  const navigate = useNavigate()
  const [params] = useSearchParams()
  // null while the probe is in flight or after it failed — the two states the
  // footer must not describe as a policy decision.
  const [status, setStatus] = useState<AuthStatus | null>(null)
  const [statusFailed, setStatusFailed] = useState(false)
  const [identifier, setIdentifier] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    fetchAuthStatus()
      .then(setStatus)
      .catch(() => setStatusFailed(true))
  }, [])

  // An instance with no accounts has nothing to sign in to yet, so send the
  // first visitor straight to the form that creates the admin.
  useEffect(() => {
    if (status?.setup_required) navigate('/register', { replace: true })
  }, [status, navigate])

  function submit(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError('')
    login(identifier, password)
      .then(() => {
        // A full navigation, not navigate(): the session cookie was just set,
        // and every page in the app loads data that needs it.
        location.assign(safeRedirect(params.get('redirect')))
      })
      .catch((err: Error) => {
        setError(err.message)
        setBusy(false)
      })
  }

  return (
    <AuthShell
      title="Sign in"
      subtitle="Publish HTML pages and share them by link."
      footer={<LoginFooter status={status} failed={statusFailed} />}
    >
      <ProviderButtons
        providers={status?.oidc_providers ?? []}
        redirect={safeRedirect(params.get('redirect'))}
      />
      <form onSubmit={submit} noValidate>
        <Field
          id="identifier"
          label="Username or email"
          autoComplete="username"
          autoFocus
          required
          value={identifier}
          onChange={(e) => setIdentifier(e.target.value)}
        />
        <Field
          id="password"
          label="Password"
          type="password"
          autoComplete="current-password"
          required
          value={password}
          onChange={(e) => setPassword(e.target.value)}
        />
        {error ? (
          <p role="alert" className="text-sm mb-4" style={{ color: 'var(--destructive, #b3261e)' }}>
            {error}
          </p>
        ) : null}
        <button
          type="submit"
          disabled={busy}
          className="btn-ink w-full h-10 rounded-lg text-sm font-medium disabled:opacity-60"
        >
          {busy ? 'Signing in…' : 'Sign in'}
        </button>
      </form>
    </AuthShell>
  )
}

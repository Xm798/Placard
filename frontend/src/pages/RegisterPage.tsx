import { useEffect, useState, type FormEvent } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import AuthShell, { Field } from './AuthShell'
import { fetchAuthStatus, register, type AuthStatus } from '../lib/auth'

export default function RegisterPage() {
  const navigate = useNavigate()
  const [status, setStatus] = useState<AuthStatus | null>(null)
  const [username, setUsername] = useState('')
  const [email, setEmail] = useState('')
  const [displayName, setDisplayName] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    fetchAuthStatus()
      .then(setStatus)
      .catch(() => setStatus(null))
  }, [])


  // Closed registration on an instance that already has accounts: there is no
  // form to show, only the way back to sign-in.
  useEffect(() => {
    if (status && !status.registration_open) navigate('/login', { replace: true })
  }, [status, navigate])

  function submit(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError('')
    register({ username, email, password, display_name: displayName })
      .then(() => location.assign('/'))
      .catch((err: Error) => {
        setError(err.message)
        setBusy(false)
      })
  }

  const firstAccount = status?.setup_required === true

  return (
    <AuthShell
      title={firstAccount ? 'Create the admin account' : 'Create an account'}
      subtitle={
        firstAccount
          ? 'This instance has no accounts yet. The first one becomes the administrator.'
          : 'Pick a username and a password to start publishing pages.'
      }
      footer={
        <>
          Already have an account?{' '}
          <Link to="/login" className="underline underline-offset-4" style={{ color: 'var(--fg)' }}>
            Sign in
          </Link>
        </>
      }
    >
      <form onSubmit={submit} noValidate>
        <Field
          id="username"
          label="Username"
          autoComplete="username"
          autoFocus
          required
          minLength={3}
          maxLength={32}
          value={username}
          onChange={(e) => setUsername(e.target.value)}
          hint="3-32 characters: letters, digits, dot, dash or underscore."
        />
        <Field
          id="display-name"
          label="Display name"
          autoComplete="name"
          maxLength={128}
          value={displayName}
          onChange={(e) => setDisplayName(e.target.value)}
          hint="Optional. Defaults to your username."
        />
        <Field
          id="email"
          label="Email"
          type="email"
          autoComplete="email"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          hint="Optional. Used to link a single sign-on identity later."
        />
        <Field
          id="password"
          label="Password"
          type="password"
          autoComplete="new-password"
          required
          minLength={8}
          maxLength={128}
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          hint="At least 8 characters. There is no password reset by email — an admin resets it."
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
          {busy ? 'Creating…' : firstAccount ? 'Create admin account' : 'Create account'}
        </button>
      </form>
    </AuthShell>
  )
}

// Local-account endpoints and the shared shape of their error responses.
//
// These three are the only calls the app makes before it has a session, so they
// deliberately do NOT go through api(): that helper redirects to /login on a
// 401, which on the login page itself is a reload loop instead of an error
// message.

export interface OIDCProvider {
  name: string
  display_name: string
}

export interface AuthStatus {
  registration_open: boolean
  setup_required: boolean
  oidc_providers?: OIDCProvider[]
}

export interface Account {
  id: string
  username: string
  display_name: string
  email?: string
  is_admin: boolean
}

// The server's error envelope (dto.ErrorResponse).
interface ErrorEnvelope {
  code?: string
  message?: string
}

async function postJSON<T>(path: string, body: unknown): Promise<T> {
  const resp = await fetch(path, {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'fetch' },
    body: JSON.stringify(body),
  })
  if (!resp.ok) {
    let message = ''
    try {
      const envelope: ErrorEnvelope = await resp.json()
      message = envelope.message || ''
    } catch {
      message = ''
    }
    throw new Error(message || 'Request failed (' + resp.status + ')')
  }
  return resp.json() as Promise<T>
}

export async function fetchAuthStatus(): Promise<AuthStatus> {
  const resp = await fetch('/api/auth/status', { credentials: 'same-origin' })
  if (!resp.ok) throw new Error('Could not load sign-in options')
  return resp.json()
}

export function login(identifier: string, password: string): Promise<Account> {
  return postJSON<Account>('/api/auth/login', { identifier, password })
}

export function register(input: {
  username: string
  email: string
  password: string
  display_name: string
}): Promise<Account> {
  return postJSON<Account>('/api/auth/register', input)
}

// safeRedirect confines the ?redirect= parameter to this origin, so the login
// page can never be turned into an open redirect.
//
// It resolves the value the way the browser will and keeps it only if it stayed
// here, rather than pattern-matching the raw string: the URL parser treats a
// backslash as a slash for http(s), so "/\evil.example" navigates off-site
// while passing any check that only looks for a leading "//".
export function safeRedirect(raw: string | null): string {
  if (!raw) return '/'
  try {
    const url = new URL(raw, location.origin)
    if (url.origin !== location.origin) return '/'
    return url.pathname + url.search + url.hash
  } catch {
    return '/'
  }
}

// oidcStartURL is where a provider button navigates. It is a full page load,
// not a fetch: the flow continues at the identity provider and comes back as a
// browser redirect.
export function oidcStartURL(provider: string, redirect: string): string {
  const params = new URLSearchParams({ provider })
  if (redirect) params.set('redirect', redirect)
  return '/auth/oidc/start?' + params.toString()
}

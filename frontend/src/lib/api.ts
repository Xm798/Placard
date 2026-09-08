// Unified request helper.
//
// Same-origin credentials (carries the session cookie) + CSRF custom header
// (X-Requested-With). A same-origin fetch auto-adds Origin + Sec-Fetch-Site:
// same-origin, satisfying the backend's CSRF triple. Non-2xx throws an Error
// carrying .status. Session-expiry is handled uniformly: the first 401 triggers
// the login redirect, concurrent 401s are silently short-circuited (dedupe guard).

export interface HttpError extends Error {
  status?: number
}

let authRedirecting = false

// Test-only: reset the module-level dedupe guard so tests are isolated.
export function __resetAuthRedirect(): void {
  authRedirecting = false
}

export async function api(path: string, opts: RequestInit = {}): Promise<Response> {
  const headers = Object.assign({ 'X-Requested-With': 'fetch' }, opts.headers || {})
  const resp = await fetch(path, Object.assign({ credentials: 'same-origin' }, opts, { headers }))
  if (resp.status === 401) {
    // The sign-in pages call the auth endpoints directly (see lib/auth), so a
    // 401 reaching here always came from the signed-in app and always means the
    // session is gone.
    if (!authRedirecting) {
      authRedirecting = true
      location.assign('/login?redirect=' + encodeURIComponent(location.pathname))
    }
    const e = new Error('http 401') as HttpError
    e.status = 401
    throw e
  }
  if (!resp.ok) {
    const e = new Error('http ' + resp.status) as HttpError
    e.status = resp.status
    throw e
  }
  return resp
}

// Date / expiry text helpers. Ported 1:1 from internal/web/app.js:47-60.

import type { TFunction } from 'i18next'

// Strip the URL scheme (http/https) for display — app.js reconstructs it as needed.
export function stripScheme(url: string): string {
  return url.replace(/^https?:\/\//, '')
}

export function pad2(n: number): string {
  return n < 10 ? '0' + n : '' + n
}

// "MM-DD HH:mm" (list creation time, local timezone)
export function fmtDateTime(iso: string): string {
  const d = new Date(iso)
  if (isNaN(d.getTime())) return ''
  return (
    pad2(d.getMonth() + 1) +
    '-' +
    pad2(d.getDate()) +
    ' ' +
    pad2(d.getHours()) +
    ':' +
    pad2(d.getMinutes())
  )
}

// "YYYY-MM-DD" (local timezone). Exported on its own so a caller writing its
// own sentence around the date interpolates the date rather than splicing in
// another translated sentence.
export function fmtDate(iso: string): string {
  const d = new Date(iso)
  if (isNaN(d.getTime())) return ''
  return d.getFullYear() + '-' + pad2(d.getMonth() + 1) + '-' + pad2(d.getDate())
}

// Expiry text. Takes the caller's `t` rather than reaching for the i18n
// singleton, so the component that renders the string is the one subscribed to
// the language it was rendered in.
export function fmtExpiry(iso: string | null, t: TFunction): string {
  if (iso == null) return t('format.neverExpires')
  const date = fmtDate(iso)
  if (date === '') return ''
  return t('format.expiresOn', { date })
}

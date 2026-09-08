import { useCallback } from 'react'
import { useTranslation } from 'react-i18next'
import { useToast } from './useToast'

// Shared clipboard helper. Promise-based (never false-positive on failure) and
// guards navigator.clipboard with ?. so callers don't repeat the boilerplate.
// Ported from copyText, app.js:219-223.
export function useCopy() {
  const { show } = useToast()
  const { t } = useTranslation()
  return useCallback((text: string, okMsg: string) => {
    navigator.clipboard?.writeText(text)
      .then(() => show(okMsg))
      .catch(() => show(t('clipboard.failed')))
  }, [show, t])
}

import { useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { Trans, useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import Dropzone from '../components/Dropzone'
import ExpirySegmented from '../components/ExpirySegmented'
import { useCopy } from '../hooks/useCopy'
import { api, type HttpError } from '../lib/api'
import { fmtDate, stripScheme } from '../lib/format'
import { visibilityHint, visibilityOptions } from '../lib/visibility'

// Retention options (app.html:246-254). Kept module-level for reuse.
const EXPIRY_VALUES = ['1d', '7d', '30d', 'never']

// val='' means "let the server decide" (user's default_visibility
// preference) — deliberately NOT appended to the publish form so the
// backend's own resolution logic (resolveVisibility) applies. Prepended here
// rather than in the shared vocabulary: it is a publish-only concept.
function visibilityOptionsWithDefault(t: TFunction) {
  return [{ val: '', label: t('publish.visibilityDefault') }, ...visibilityOptions(t)]
}

function visibilityHintWithDefault(val: string, t: TFunction): string {
  return val === '' ? t('publish.visibilityDefaultHint') : visibilityHint(val, t)
}

// Publish page. Ported 1:1 from internal/web/app.html:214-299 + app.js:142-227.

const MAX_BYTES = 10 * 1024 * 1024

// app.js:196-204 — status → error copy. Exported for unit testing.
export function publishErrMsg(status: number | undefined, t: TFunction): string {
  switch (status) {
    case 413:
      return t('publish.errors.tooLarge')
    case 415:
      return t('publish.errors.onlyHTML')
    case 429:
      return t('publish.errors.rateLimited')
    case 401:
      return t('publish.errors.sessionExpired')
    default:
      return t('publish.errors.generic')
  }
}

type Card = 'form' | 'uploading' | 'error' | 'success'

interface PublishResult {
  display: string
  url: string
  expiryText: string
  version: number
}

function scrollTop() {
  try {
    window.scrollTo({ top: 0, behavior: 'smooth' })
  } catch {
    // jsdom / unsupported — no-op
  }
}

export default function PublishPage() {
  const copy = useCopy()
  const { t } = useTranslation()
  const [searchParams, setSearchParams] = useSearchParams()
  const updateId = searchParams.get('update') || ''
  const [card, setCard] = useState<Card>('form')
  const [title, setTitle] = useState('')
  const [expiry, setExpiry] = useState('never')
  const [visibility, setVisibility] = useState('')
  const [uploadingName, setUploadingName] = useState('')
  const [errorMsg, setErrorMsg] = useState('')
  const [result, setResult] = useState<PublishResult | null>(null)
  const [dropKey, setDropKey] = useState(0)

  function showCard(c: Card) {
    setCard(c)
    scrollTop()
  }

  function failUpload(msg: string) {
    setErrorMsg(msg)
    showCard('error')
  }

  function succeedUpload(data: { url?: string; expires_at?: string | null; version?: number }) {
    // data: { id, url, title, version, expires_at, create_time }
    const url = data.url || ''
    const display = stripScheme(url)
    const expiryText =
      data.expires_at == null
        ? t('publish.linkForever')
        : t('publish.linkUntil', { date: fmtDate(data.expires_at) })
    setResult({ display, url, expiryText, version: data.version || 1 })
    showCard('success')
  }

  function publish(file: File) {
    if (!file) return
    const okType = /\.html?$/.test(file.name) || file.type === 'text/html'
    if (!okType) {
      return failUpload(t('publish.errors.localType'))
    }
    if (file.size > MAX_BYTES) {
      return failUpload(publishErrMsg(413, t))
    }
    setUploadingName(file.name)
    showCard('uploading')

    const fd = new FormData()
    fd.append('file', file)
    fd.append('title', title.trim())
    fd.append('expiry', expiry)
    if (visibility) fd.append('visibility', visibility)
    if (updateId) fd.append('id', updateId)
    api('/api/publish', { method: 'POST', body: fd })
      .then((r) => r.json())
      .then((data) => succeedUpload(data))
      .catch((e: HttpError) => failUpload(publishErrMsg(e.status, t)))
  }

  function resetUpload() {
    setDropKey((k) => k + 1) // remount Dropzone -> clears its file input
    showCard('form')
  }

  // Update-mode success card only: clear ?update= so the next drop starts a
  // brand-new publish instead of another version of the same page.
  function startFreshPublish() {
    setSearchParams({})
    resetUpload()
  }

  function copyUrl() {
    // result.display holds the origin-stripped form (see succeedUpload);
    // reconstruct https:// exactly as app.js:226 does (not result.url).
    copy('https://' + (result?.display ?? ''), t('publish.copied'))
  }

  return (
    <section id="page-publish">
      {card === 'form' && (
        <div id="publish-form">
          {/* Hero */}
          <div className="pt-20 pb-8 sm:pt-24 sm:pb-10">
            <h1 className="font-display font-extrabold tracking-[-0.03em] leading-[1.0] text-4xl sm:text-5xl mb-4">
              {updateId ? t('publish.titleUpdate') : t('publish.titleNew')}
            </h1>
            <p className="text-base text-soft max-w-2xl leading-relaxed">
              <Trans
                i18nKey={updateId ? 'publish.leadUpdate' : 'publish.leadNew'}
                values={{ id: updateId }}
                components={{
                  code: <span className="mono text-[0.9em]" style={{ color: 'var(--fg)' }} />,
                }}
              />
            </p>
          </div>

          {/* Dropzone */}
          <Dropzone key={dropKey} onFile={publish} />

          {/* Options bar */}
          <div className="mt-5 flex flex-col sm:flex-row gap-4 sm:items-center sm:justify-between text-sm">
            <div className="flex items-center gap-2">
              <span className="text-faint">{t('publish.titleField')}</span>
              <input
                type="text"
                id="publish-title"
                aria-label={t('publish.titleField')}
                className="bg-transparent border-b border-line-strong focus:border-[var(--fg)] outline-none px-1 py-1 w-56 transition-colors"
                placeholder={t('publish.titlePlaceholder')}
                value={title}
                onChange={(e) => setTitle(e.target.value)}
              />
            </div>
            <div className="flex items-center gap-2">
              <span className="text-faint" id="expiry-label">
                {t('publish.expiryLabel')}
              </span>
              <ExpirySegmented
                options={EXPIRY_VALUES.map((val) => ({ val, label: t('publish.expiry.' + val) }))}
                value={expiry}
                onChange={setExpiry}
                ariaLabelledBy="expiry-label"
              />
            </div>
            <div className="flex items-center gap-2">
              <span className="text-faint" id="visibility-label">
                {t('publish.visibilityLabel')}
              </span>
              <ExpirySegmented
                options={visibilityOptionsWithDefault(t)}
                value={visibility}
                onChange={setVisibility}
                ariaLabelledBy="visibility-label"
              />
              <span className="text-faint text-xs leading-relaxed">
                {visibilityHintWithDefault(visibility, t)}
              </span>
            </div>
          </div>
        </div>
      )}

      {/* Uploading */}
      {card === 'uploading' && (
        <div id="uploading-card" className="pt-24 pb-16" role="status" aria-live="polite">
          <div className="max-w-lg mx-auto text-center">
            <div className="mx-auto w-12 h-12 mb-5">
              <svg
                className="w-12 h-12 animate-spin"
                style={{ color: 'var(--fg-soft)' }}
                fill="none"
                viewBox="0 0 24 24"
                aria-hidden="true"
              >
                <circle cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="3" opacity=".25" />
                <path
                  d="M12 2a10 10 0 0 1 10 10"
                  stroke="currentColor"
                  strokeWidth="3"
                  strokeLinecap="round"
                />
              </svg>
            </div>
            <p className="font-display font-semibold text-lg">{t('publish.uploading')}</p>
            <p className="text-soft text-sm mt-1" id="uploading-name">
              {uploadingName}
            </p>
          </div>
        </div>
      )}

      {/* Error */}
      {card === 'error' && (
        <div id="error-card" className="pt-24 pb-16" role="alert">
          <div className="max-w-lg mx-auto">
            <div className="flex items-center gap-2.5 mb-2">
              <svg
                className="w-5 h-5"
                style={{ color: '#dc2626' }}
                fill="none"
                viewBox="0 0 24 24"
                stroke="currentColor"
                strokeWidth="2.5"
                aria-hidden="true"
              >
                <path d="M12 9v4m0 4h.01M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z" />
              </svg>
              <span className="font-display font-bold text-2xl tracking-tight">{t('publish.errorTitle')}</span>
            </div>
            <p className="text-soft text-sm mb-6" id="error-msg">
              {errorMsg}
            </p>
            <button
              data-action="reset-upload"
              className="btn-ink rounded-lg text-sm font-medium px-4 py-2"
              onClick={resetUpload}
            >
              {t('publish.retry')}
            </button>
          </div>
        </div>
      )}

      {/* Success */}
      {card === 'success' && (
        <div id="success-card" className="pt-24 pb-16">
          <div className="max-w-lg mx-auto">
            <div className="flex items-center gap-2.5 mb-2">
              <svg
                className="w-5 h-5"
                style={{ color: '#10b981' }}
                fill="none"
                viewBox="0 0 24 24"
                stroke="currentColor"
                strokeWidth="2.5"
                aria-hidden="true"
              >
                <path d="M5 13l4 4L19 7" />
              </svg>
              <span className="font-display font-bold text-2xl tracking-tight">
                {result && result.version > 1
                  ? t('publish.successVersion', { version: result.version })
                  : t('publish.successTitle')}
              </span>
            </div>
            <p className="text-soft text-sm mb-2" id="success-expiry">
              {result?.expiryText}
            </p>
            <div className="flex items-center gap-2 p-2 pl-4 rounded-xl border border-line-strong surface mb-5">
              <code className="flex-1 mono text-sm truncate" id="success-url">
                {result?.display}
              </code>
              <button
                data-action="copy-url"
                aria-label={t('publish.copyLinkAria')}
                className="btn-ink shrink-0 rounded-lg text-sm font-medium px-4 py-2"
                onClick={copyUrl}
              >
                {t('publish.copyLink')}
              </button>
            </div>
            <div className="flex gap-2">
              <button
                data-action="reset-upload"
                className="btn-outline rounded-lg text-sm font-medium px-4 py-2"
                onClick={resetUpload}
              >
                {t('publish.continue')}
              </button>
              <a
                id="success-open"
                href={result?.url || '#'}
                target="_blank"
                rel="noopener"
                className="btn-outline rounded-lg text-sm font-medium px-4 py-2 inline-flex items-center"
              >
                {t('publish.openNewTab')}
              </a>
              {updateId && (
                <button
                  data-action="start-fresh-publish"
                  className="btn-outline rounded-lg text-sm font-medium px-4 py-2"
                  onClick={startFreshPublish}
                >
                  {t('publish.publishNew')}
                </button>
              )}
            </div>
          </div>
        </div>
      )}
    </section>
  )
}

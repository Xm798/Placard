import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

// Same-origin avatar proxy consumer. Extracted from Header's inline logic
// (was: me.avatar_url ? <img> : initial) so it can be reused by the grants
// roster and the people-search results. Never consumes an image URL from any
// API response — always builds the proxy path itself from authzId, per the
// avatar-proxy contract (no image field exists on grants/search/me bodies).
interface AvatarProps {
  authzId?: string
  name?: string
  className?: string
}

export default function Avatar({ authzId, name, className }: AvatarProps) {
  const { t } = useTranslation()
  const [errored, setErrored] = useState(false)
  const src = authzId ? '/api/users/' + encodeURIComponent(authzId) + '/avatar' : ''

  // A fresh authzId (e.g. scrolling to a new row) deserves a fresh attempt —
  // don't let a previous row's 404 stick to this one.
  useEffect(() => {
    setErrored(false)
  }, [src])

  const showImg = src !== '' && !errored
  // className fully replaces the default sizing (w-8 h-8 text-sm) rather than
  // appending — two conflicting Tailwind size utilities in one class list
  // don't reliably resolve by string order, only by generated-CSS order.
  const sizeClass = className || 'w-8 h-8 text-sm'

  return (
    <div
      className={
        'rounded-full flex items-center justify-center font-semibold overflow-hidden shrink-0 ' + sizeClass
      }
      style={{ background: 'var(--ink)', color: 'var(--ink-fg)' }}
    >
      {showImg ? (
        <img
          src={src}
          alt={name || t('avatar.alt')}
          className="w-full h-full object-cover"
          referrerPolicy="no-referrer"
          onError={() => setErrored(true)}
        />
      ) : name ? (
        name.slice(0, 1).toUpperCase()
      ) : (
        'C'
      )}
    </div>
  )
}

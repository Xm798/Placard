import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'

export function LogoMark({ className = 'w-10 h-10 text-sm' }: { className?: string }) {
  return (
    <div className={'logo-mark flex items-center justify-center mono font-black ' + className} aria-hidden="true">
      &lt;/&gt;
    </div>
  )
}

export function Wordmark() {
  return <span className="font-display text-2xl uppercase">Placard</span>
}

export default function Brand({ onClick }: { onClick?: () => void }) {
  const { t } = useTranslation()
  return (
    <Link to="/" aria-label={t('header.home')} className="flex items-center gap-3" onClick={onClick}>
      <LogoMark />
      <Wordmark />
    </Link>
  )
}

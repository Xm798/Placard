import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'

export default function NotFoundPage() {
  const { t } = useTranslation()
  return (
    <div className="py-24 text-center">
      <p className="text-lg font-medium mb-4">{t('notFound.title')}</p>
      <Link to="/" className="ul text-sm">
        {t('notFound.back')}
      </Link>
    </div>
  )
}

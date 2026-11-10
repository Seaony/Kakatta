import { createFileRoute } from '@tanstack/react-router';
import { useTranslation } from '../i18n';

function Foundation() {
  const { t } = useTranslation();
  return (
    <section className="max-w-2xl space-y-lg" aria-labelledby="page-title">
      <h1 id="page-title" className="text-page font-semibold text-text-primary">{t('app.name')}</h1>
      <h2 className="text-section font-medium text-text-primary">{t('web.foundation.title')}</h2>
      <p className="text-body text-text-secondary">{t('web.foundation.description')}</p>
    </section>
  );
}

export const Route = createFileRoute('/')({ component: Foundation });

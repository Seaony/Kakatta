import { createRootRouteWithContext, Link, Outlet, type ErrorComponentProps } from '@tanstack/react-router';
import type { RouterContext } from '../app/router';
import { useTranslation, formatMessage } from '../i18n';
import { ApiError } from '../api/errors';
import { StatePanel } from '../components/ui/state-panel';
import { Gateway } from '../app/gateway';
import { InvalidSearchError } from '../app/search';
import { UnsavedChangesProvider } from '../app/unsaved-changes';

function Root() {
  const { t } = useTranslation();
  return (
    <>
      <a className="fixed left-lg top-lg z-[100] -translate-y-[200%] rounded-control bg-accent px-md py-sm text-on-accent focus:translate-y-0" href="#main">{t('common.skip_to_content')}</a>
      <UnsavedChangesProvider><Gateway><Outlet /></Gateway></UnsavedChangesProvider>
    </>
  );
}

export function RootError({ error, reset }: ErrorComponentProps) {
  const { t, locale } = useTranslation();
  let description = t('web.error.description');
  if (error instanceof ApiError) {
    try { description = formatMessage(error.reason, locale); } catch { description = t('web.request_failed'); }
  }
  if (error instanceof InvalidSearchError || error instanceof Error && error.cause instanceof InvalidSearchError) return <div className="space-y-md"><StatePanel kind="error" title={t('shell.invalid_search_title')} description={t('shell.invalid_search_description')} /><a className="inline-flex min-h-control items-center rounded-control bg-accent px-md text-on-accent" href={window.location.pathname}>{t('shell.reset_search')}</a></div>;
  return <StatePanel kind="error" title={t('web.error.title')} description={description} retry={{ label: t('common.retry'), run: reset }} />;
}

export function NotFound() {
  const { t } = useTranslation();
  return <StatePanel kind="empty" title={t('web.not_found.title')} description={t('web.not_found.description')} action={<Link className="inline-flex min-h-control items-center rounded-control bg-accent px-md text-on-accent" to="/">{t('common.back_home')}</Link>} />;
}

export const Route = createRootRouteWithContext<RouterContext>()({
  component: Root,
  errorComponent: RootError,
  notFoundComponent: NotFound,
});

import { createRootRouteWithContext, Link, Outlet, type ErrorComponentProps } from '@tanstack/react-router';
import type { RouterContext } from '../app/router';
import { useTranslation, formatMessage } from '../i18n';
import { ApiError } from '../api/errors';
import { StatePanel } from '../components/ui/state-panel';

function Root() {
  const { t } = useTranslation();
  return (
    <>
      <a className="fixed left-lg top-lg z-[100] -translate-y-[200%] rounded-control bg-accent px-md py-sm text-on-accent focus:translate-y-0" href="#main">{t('common.skip_to_content')}</a>
      <main id="main" className="mx-auto w-full max-w-5xl px-lg py-xxl outline-none" tabIndex={-1}>
        <Outlet />
      </main>
    </>
  );
}

export function RootError({ error, reset }: ErrorComponentProps) {
  const { t, locale } = useTranslation();
  let description = t('web.error.description');
  if (error instanceof ApiError) {
    try { description = formatMessage(error.reason, locale); } catch { description = t('web.request_failed'); }
  }
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

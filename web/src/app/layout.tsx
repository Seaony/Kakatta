import { useState, type ReactNode } from 'react';
import { Link } from '@tanstack/react-router';
import type { Schema } from '../api/types';
import { Button } from '../components/ui/button';
import { Icon } from '../components/icons';
import { useTranslation } from '../i18n';

const primaryNavigation = [
  { to: '/overview', label: 'shell.nav.overview', icon: 'overview' },
  { to: '/results', label: 'shell.nav.results', icon: 'results' },
  { to: '/tasks', label: 'shell.nav.tasks', icon: 'tasks' },
  { to: '/logs', label: 'shell.nav.logs', icon: 'logs' },
  { to: '/accounts', label: 'shell.nav.accounts', icon: 'accounts' },
] as const;

const settingsNavigation = [
  { to: '/settings/general', label: 'shell.nav.general', icon: 'general' },
  { to: '/settings/ai', label: 'shell.nav.ai', icon: 'ai' },
  { to: '/settings/notifications', label: 'shell.nav.notifications', icon: 'notifications' },
  { to: '/settings/limits', label: 'shell.nav.limits', icon: 'limits' },
  { to: '/settings/prompts', label: 'shell.nav.prompts', icon: 'prompts' },
  { to: '/settings/security', label: 'shell.nav.security', icon: 'security' },
  { to: '/settings/system', label: 'shell.nav.system', icon: 'system' },
  { to: '/settings/backup', label: 'shell.nav.backup', icon: 'backup' },
  { to: '/settings/about', label: 'shell.nav.about', icon: 'about' },
] as const;

export function PublicLayout({ children }: { children: ReactNode }) {
  return <main id="main" tabIndex={-1} className="mx-auto w-full max-w-3xl px-lg py-xl outline-none sm:px-xl sm:py-xxl">{children}</main>;
}

export function AppShell({ user, children, onLogout, loggingOut, logoutError }: {
  user: Schema<'Me'>;
  children: ReactNode;
  onLogout(): void;
  loggingOut: boolean;
  logoutError: string | undefined;
}) {
  const { t } = useTranslation();
  const [navigationOpen, setNavigationOpen] = useState(false);
  const linkClass = 'flex min-h-control min-w-0 items-center gap-sm rounded-control px-md py-sm text-body text-text-body [@media(hover:hover)]:hover:bg-control focus-visible:outline-focus';
  return (
    <div className="mx-auto grid min-h-screen w-full max-w-[112rem] min-w-0 lg:grid-cols-[14rem_minmax(0,1fr)]">
      <aside className="min-w-0 border-b border-border bg-sidebar px-lg py-md lg:sticky lg:top-0 lg:flex lg:h-screen lg:flex-col lg:overflow-y-auto lg:border-b-0 lg:border-r lg:py-xl">
        <div className="flex min-h-control items-center justify-between gap-md lg:mb-xl">
          <Link to="/overview" className="text-section font-semibold text-text-primary" onClick={() => setNavigationOpen(false)}>{t('app.name')}</Link>
          <Button variant="plain" className="lg:hidden" aria-controls="application-navigation" aria-expanded={navigationOpen} onClick={() => setNavigationOpen((open) => !open)}>
            <Icon name={navigationOpen ? 'close' : 'menu'} />
            {navigationOpen ? t('common.close') : t('shell.open_navigation')}
          </Button>
        </div>
        <nav id="application-navigation" aria-label={t('shell.nav_label')} className={`${navigationOpen ? 'block' : 'hidden'} min-w-0 space-y-xl pt-lg lg:block lg:pt-0`}>
          <div className="space-y-xs">
            {primaryNavigation.map((item) => <Link key={item.to} to={item.to} className={linkClass} activeProps={{ className: 'bg-sidebar-selection font-semibold text-text-primary' }} onClick={() => setNavigationOpen(false)}><Icon name={item.icon} /><span className="min-w-0 break-words">{t(item.label)}</span></Link>)}
          </div>
          <section aria-labelledby="settings-navigation-title" className="space-y-sm">
            <h2 id="settings-navigation-title" className="px-md text-caption font-medium text-text-secondary">{t('shell.nav.settings')}</h2>
            <div className="space-y-xs">
              {settingsNavigation.map((item) => <Link key={item.to} to={item.to} className={linkClass} activeProps={{ className: 'bg-sidebar-selection font-semibold text-text-primary' }} onClick={() => setNavigationOpen(false)}><Icon name={item.icon} /><span className="min-w-0 break-words">{t(item.label)}</span></Link>)}
            </div>
          </section>
          <div className="space-y-sm border-t border-border pt-lg lg:hidden">
            <p className="break-words text-body text-text-secondary">{user.username}</p>
            <Button variant="plain" busy={loggingOut} onClick={onLogout}>{t('shell.logout')}</Button>
            {logoutError && <p role="alert" className="text-body text-danger">{logoutError}</p>}
          </div>
        </nav>
        <div className="mt-auto hidden space-y-sm border-t border-border pt-lg lg:block">
          <p className="break-words text-body text-text-secondary">{user.username}</p>
          <Button variant="plain" busy={loggingOut} onClick={onLogout}>{t('shell.logout')}</Button>
          {logoutError && <p role="alert" className="text-body text-danger">{logoutError}</p>}
        </div>
      </aside>
      <main id="main" tabIndex={-1} className="min-w-0 px-lg py-xl outline-none sm:px-xl lg:px-xxl lg:py-xxl">{children}</main>
    </div>
  );
}

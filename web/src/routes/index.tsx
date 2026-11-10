import { createFileRoute, Navigate } from '@tanstack/react-router';
import { useTranslation } from '../i18n';
import { useQuery } from '@tanstack/react-query';
import { apiData, useApi, useApiError } from '../api/hooks';
import { apiKey } from '../api/keys';
import { StatePanel } from '../components/ui/state-panel';

function Start() {
  const { t } = useTranslation();
  const api = useApi();
  const describe = useApiError();
  const settings = useQuery({ queryKey: apiKey('settings'), queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/settings', { signal })) });
  if (settings.isPending) return <StatePanel kind="pending" label={t('common.loading')} />;
  if (settings.isError) return <StatePanel kind="error" title={t('web.error.title')} description={describe(settings.error)} retry={{ label: t('common.retry'), run: () => { void settings.refetch(); } }} />;
  return <Navigate to={settings.data.values['general.onboarding_done'] === true ? '/overview' : '/onboarding'} replace />;
}

export const Route = createFileRoute('/')({ component: Start });

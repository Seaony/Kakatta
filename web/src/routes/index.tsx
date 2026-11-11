import { memo } from 'react';
import { createFileRoute, Navigate as RouterNavigate } from '@tanstack/react-router';
import { useTranslation } from '../i18n';
import { useQuery } from '@tanstack/react-query';
import { apiData, useApi, useApiError } from '../api/hooks';
import { apiKey } from '../api/keys';
import { StatePanel } from '../components/ui/state-panel';
import { useAuthentication } from '../app/auth-state';

// 同一目标的跳转在路由提交前不能随重新渲染重复发起。
const Navigate = memo(RouterNavigate);

function Start() {
  const { t } = useTranslation();
  const api = useApi();
  const describe = useApiError();
  const authentication = useAuthentication();
  const settings = useQuery({ queryKey: apiKey('settings'), queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/settings', { signal })), enabled: authentication?.authenticated === true });
  if (authentication?.authenticated !== true || settings.isPending) return <StatePanel kind="pending" label={t('common.loading')} />;
  if (settings.isError) return <StatePanel kind="error" title={t('web.error.title')} description={describe(settings.error)} retry={{ label: t('common.retry'), run: () => { void settings.refetch(); } }} />;
  return <Navigate to={settings.data.values['general.onboarding_done'] === true ? '/overview' : '/onboarding'} replace />;
}

export const Route = createFileRoute('/')({ component: Start });

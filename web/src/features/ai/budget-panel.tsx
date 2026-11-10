import { useQuery } from '@tanstack/react-query';
import { apiData, useApi, useTimeZone } from '../../api/hooks';
import { apiKey } from '../../api/keys';
import { Button, InlineMessage, Skeleton } from '../../components/ui';
import { useTranslation } from '../../i18n';
import { formatDateTime, formatInteger } from '../../i18n/format';
import { AIErrorNotice } from './error-notice';

export function AIBudgetPanel() {
  const api = useApi();
  const { t, locale } = useTranslation();
  const timeZone = useTimeZone();
  const budget = useQuery({ queryKey: apiKey('ai-usage', 'budget'), queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/ai/budget', { signal })) });
  if (budget.isError && !budget.data) return <div className="space-y-sm"><AIErrorNotice error={budget.error} /><Button onClick={() => { void budget.refetch(); }}>{t('common.retry')}</Button></div>;
  if (!budget.data) return <div aria-busy="true" aria-label={t('common.loading')}><Skeleton /></div>;
  const current = budget.data;
  return <div className="space-y-md">
    <p className="text-body text-text-secondary">{current.limit === 0 ? t('web.ai.budget_unlimited', { used: formatInteger(current.protected_used, locale), count: current.protected_used }) : t('web.ai.budget_used', { used: formatInteger(current.protected_used, locale), limit: formatInteger(current.limit, locale) })}</p>
    {timeZone && <p className="text-caption text-text-secondary">{t('web.ai.budget_window', { time: formatDateTime(current.protected_start, locale, timeZone) })}</p>}
    {current.blocked && <InlineMessage tone="warning" action={<Button onClick={() => document.getElementById('ai-daily-token-limit')?.focus()}>{t('web.ai.change_limit')}</Button>}>
      <p>{current.next_check_at !== null && timeZone ? t('web.ai.budget_wait', { limit: formatInteger(current.limit, locale), time: formatDateTime(current.next_check_at, locale, timeZone) }) : t('web.ai.budget_wait_unknown')}</p>
      <p>{t('web.ai.resume_next_run')}</p>
    </InlineMessage>}
    {budget.isError && <div className="space-y-sm"><AIErrorNotice error={budget.error} /><Button onClick={() => { void budget.refetch(); }}>{t('common.retry')}</Button></div>}
  </div>;
}

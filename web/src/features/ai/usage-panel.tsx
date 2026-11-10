import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { apiData, useApi } from '../../api/hooks';
import { apiKey } from '../../api/keys';
import type { RequestQuery, Schema } from '../../api/types';
import { Button, DataTable, FormSection, StatePanel, Tabs, type DataColumn } from '../../components/ui';
import { useTranslation } from '../../i18n';
import { formatDateTime, formatInteger } from '../../i18n/format';
import { AIErrorNotice, useAIErrorDescription } from './error-notice';
import { UsageTotals } from './usage-totals';

export function AIUsagePanel() {
  const api = useApi();
  const { t, locale } = useTranslation();
  const errorText = useAIErrorDescription();
  const [window, setWindow] = useState<NonNullable<RequestQuery<'getAIUsage'>['window']>>('today');
  const usage = useQuery({ queryKey: apiKey('ai-usage', 'report', window), queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/ai/usage', { signal, params: { query: { window } } })) });
  function purposeName(purpose: string): string {
    switch (purpose) {
      case 'title_screen': return t('web.ai.prompt_title_screen');
      case 'analysis': return t('web.ai.purpose_analysis');
      case 'profile': return t('web.ai.purpose_profile');
      case 'market': return t('web.ai.prompt_market');
      case 'test': return t('web.ai.test');
      case 'probe': return t('web.ai.usage_probe');
      default: return t('web.ai.purpose_fallback', { purpose });
    }
  }
  const taskColumns: DataColumn<Schema<'TaskUsage'>>[] = [
    { key: 'task', header: t('web.ai.by_task'), cell: (row) => row.task_id === null ? t('web.ai.usage_unassigned') : row.deleted ? t('web.ai.deleted_task', { id: row.task_id }) : row.task_name ?? t('web.ai.task_id', { id: row.task_id }) },
    { key: 'input', header: t('web.ai.input_tokens'), align: 'right', cell: (row) => formatInteger(row.total.input_tokens, locale) },
    { key: 'output', header: t('web.ai.output_tokens'), align: 'right', cell: (row) => formatInteger(row.total.output_tokens, locale) },
    { key: 'total', header: t('web.ai.total_tokens'), align: 'right', cell: (row) => formatInteger(row.total.input_tokens + row.total.output_tokens, locale) },
  ];
  const purposeColumns: DataColumn<Schema<'PurposeUsage'>>[] = [
    { key: 'purpose', header: t('web.ai.by_purpose'), cell: (row) => purposeName(row.purpose) },
    { key: 'input', header: t('web.ai.input_tokens'), align: 'right', cell: (row) => formatInteger(row.total.input_tokens, locale) },
    { key: 'output', header: t('web.ai.output_tokens'), align: 'right', cell: (row) => formatInteger(row.total.output_tokens, locale) },
    { key: 'total', header: t('web.ai.total_tokens'), align: 'right', cell: (row) => formatInteger(row.total.input_tokens + row.total.output_tokens, locale) },
  ];
  const content = !usage.data && usage.isPending ? <StatePanel kind="pending" label={t('common.loading')} /> : !usage.data ? <StatePanel kind="error" title={t('web.error.title')} description={errorText(usage.error)} retry={{ label: t('common.retry'), run: () => { void usage.refetch(); } }} /> : <div className="min-w-0 space-y-xl" aria-busy={usage.isFetching}>
      <p className="text-body text-text-secondary">{t('web.ai.usage_window', { start: formatDateTime(usage.data.from, locale, usage.data.timezone), end: formatDateTime(usage.data.until, locale, usage.data.timezone), timezone: usage.data.timezone })}</p>
      <UsageTotals inputTokens={usage.data.total.input_tokens} outputTokens={usage.data.total.output_tokens} reasoningTokens={usage.data.total.reasoning_tokens} />
      {usage.data.total.calls === 0 && <p className="text-body text-text-secondary">{t('web.ai.no_usage')}</p>}
      {usage.data.incomplete && <p className="text-body text-warning">{t('web.ai.usage_coverage')}</p>}
      <p className="text-body text-text-secondary">{t('web.ai.usage_incomplete')}</p>
      <DataTable label={t('web.ai.by_task')} rows={usage.data.by_task} columns={taskColumns} rowKey={(row) => row.task_id ?? 'unassigned'} empty={t('web.ai.no_usage')} busy={usage.isFetching} />
      <DataTable label={t('web.ai.by_purpose')} rows={usage.data.by_purpose} columns={purposeColumns} rowKey={(row) => row.purpose} empty={t('web.ai.no_usage')} busy={usage.isFetching} />
      {usage.isError && <AIErrorNotice error={usage.error} />}
      {usage.isError && <Button busy={usage.isFetching} onClick={() => { void usage.refetch(); }}>{t('common.retry')}</Button>}
    </div>;
  return <FormSection title={t('web.ai.usage')}>
    <Tabs label={t('web.ai.usage')} value={window} onValueChange={(value) => { if (value === 'today' || value === '7d' || value === '30d') setWindow(value); }} items={[
      { value: 'today', label: t('web.ai.today'), ...(window === 'today' ? { content } : {}) },
      { value: '7d', label: t('web.ai.seven_days'), ...(window === '7d' ? { content } : {}) },
      { value: '30d', label: t('web.ai.thirty_days'), ...(window === '30d' ? { content } : {}) },
    ]} />
  </FormSection>;
}

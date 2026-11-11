import { useEffect, useRef, useState, type FormEvent } from 'react';
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { apiData, useApi, useMessageText } from '../../api/hooks';
import { apiKey } from '../../api/keys';
import type { Schema } from '../../api/types';
import { ApiError } from '../../api/errors';
import { Button, FormSection, SelectField, Skeleton, StatePanel, TextField } from '../../components/ui';
import { useTranslation } from '../../i18n';
import { aiSettingKeys, parseSettingInteger, type AISettingKey, type AISettingsDraft } from './draft';
import { AIErrorNotice, useAIErrorDescription } from './error-notice';
import { useUnsavedChanges } from '../../app/unsaved-changes';
import { fieldWarnings } from '../settings-general/model';

export function AISettingsPanel() {
  const api = useApi();
  const { t } = useTranslation();
  const errorText = useAIErrorDescription();
  const messageText = useMessageText();
  const queryClient = useQueryClient();
  const [draft, setDraft] = useState<AISettingsDraft>({});
  const [localErrors, setLocalErrors] = useState<Partial<Record<AISettingKey, string>>>({});
  const [saved, setSaved] = useState(false);
  const formRef = useRef<HTMLFormElement>(null);
  const snapshot = useQuery({ queryKey: apiKey('settings'), queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/settings', { signal })) });
  const definitions = useQuery({ queryKey: apiKey('setting-definitions'), queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/settings/definitions', { signal })) });
  const proxies = useInfiniteQuery({
    queryKey: apiKey('proxies'), initialPageParam: null as string | null,
    queryFn: async ({ signal, pageParam }) => apiData(await api.GET('/api/v1/proxies', { signal, params: { query: { limit: 200, ...(pageParam ? { cursor: pageParam } : {}) } } })),
    getNextPageParam: (lastPage) => lastPage.next_cursor,
  });
  const save = useMutation({
    mutationFn: async (values: Schema<'SettingValues'>) => apiData(await api.PATCH('/api/v1/settings', { body: { values } })),
    onSuccess: async (next) => {
      queryClient.setQueryData(apiKey('settings'), next);
      setDraft({}); setLocalErrors({}); setSaved(true);
      await Promise.all(['ai-usage', 'system-events', 'tasks'].map((resource) => queryClient.invalidateQueries({ queryKey: apiKey(resource) })));
    },
  });
  const dirty = aiSettingKeys.some((key) => draft[key] !== undefined && draft[key] !== String(snapshot.data?.values[key] ?? ''));
  useUnsavedChanges(dirty);
  useEffect(() => {
    if (save.error instanceof ApiError && save.error.fields.length > 0) formRef.current?.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus();
  }, [save.error]);
  useEffect(() => {
    if (Object.keys(localErrors).length > 0) formRef.current?.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus();
  }, [localErrors]);
  function update(key: AISettingKey, value: string) {
    setDraft((current) => ({ ...current, [key]: value }));
    setLocalErrors((current) => { const next = { ...current }; delete next[key]; return next; });
    setSaved(false);
  }
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (save.isPending) return;
    const values: Schema<'SettingValues'> = {};
    const errors: Partial<Record<AISettingKey, string>> = {};
    for (const key of aiSettingKeys) {
      const value = draft[key];
      if (value === undefined) continue;
      try { values[key] = parseSettingInteger(key, value); }
      catch { errors[key] = t('web.ai.field_integer'); }
    }
    setLocalErrors(errors);
    if (Object.keys(errors).length === 0 && Object.keys(values).length > 0) save.mutate(values);
  }
  const initialPending = !snapshot.data && snapshot.isPending || !definitions.data && definitions.isPending;
  if (initialPending) return <StatePanel kind="pending" label={t('common.loading')}><Skeleton /><Skeleton /></StatePanel>;
  if (!snapshot.data || !definitions.data) return <StatePanel kind="error" title={t('web.error.title')} description={errorText(snapshot.error ?? definitions.error)} retry={{ label: t('common.retry'), run: () => { void snapshot.refetch(); void definitions.refetch(); } }} />;
  const settings = snapshot.data;
  const fieldDefinitions = definitions.data;
  const proxyOptions = proxies.data?.pages.flatMap((page) => page.items) ?? [];
  const currentProxy = draft['ai.proxy_id'] ?? String(settings.values['ai.proxy_id'] ?? '');
  const fieldIssues = save.error instanceof ApiError ? save.error.fields : [];
  return (
    <FormSection title={t('web.ai.configuration')}>
      <form ref={formRef} onSubmit={submit} className="min-w-0 space-y-xl" aria-busy={save.isPending}>
        {(snapshot.isError || definitions.isError) && <p role="alert" className="text-body text-danger">{errorText(snapshot.error ?? definitions.error)}</p>}
        {aiSettingKeys.map((key) => {
          const definition = fieldDefinitions.find((item) => item.key === key);
          if (!definition) return <p role="alert" key={key}>{t('web.invalid_response')}</p>;
          const value = draft[key] ?? String(settings.values[key] ?? '');
          const fieldError = localErrors[key] ?? fieldIssues.find((issue) => issue.key === key)?.reason;
          const error = typeof fieldError === 'string' ? fieldError : fieldError ? messageText(fieldError) : undefined;
          const disabled = save.isPending || settings.sources[key] === 'environment';
          const warnings = fieldWarnings(definition, { kind: 'scalar', text: value }, draft[key] === undefined ? settings.warnings.filter((warning) => warning.key === key).map((warning) => warning.reason) : []);
          const common = { label: messageText(definition.label), hint: messageText(definition.why), ...(error ? { error } : {}) };
          const defaultValue = definition.default === null ? t('web.ai.no_proxy') : `${String(definition.default)}${definition.unit ? ` ${messageText({ code: `units.${definition.unit}`, args: { count: typeof definition.default === 'number' ? definition.default : 2 } })}` : ''}`;
          return <div key={key} className="min-w-0 space-y-sm">
            {key === 'ai.proxy_id' ? <>
              <SelectField {...common} disabled={disabled || !proxies.data} value={currentProxy} onValueChange={(next) => update(key, next)} options={[
                { value: '', label: t('web.ai.no_proxy') },
                ...proxyOptions.map((proxy) => ({ value: String(proxy.id), label: proxy.name, disabled: !proxy.configured })),
                ...(currentProxy && !proxyOptions.some((proxy) => String(proxy.id) === currentProxy) ? [{ value: currentProxy, label: t('web.ai.proxy_missing'), disabled: true }] : []),
              ]} />
              {proxies.isError && <p role="alert" className="text-body text-danger">{errorText(proxies.error)} <Button onClick={() => { void proxies.refetch(); }}>{t('common.retry')}</Button></p>}
              {proxies.hasNextPage && <Button busy={proxies.isFetchingNextPage} onClick={() => { void proxies.fetchNextPage(); }}>{t('web.ai.load_more_proxies')}</Button>}
            </> : <TextField {...common} {...(key === 'ai.daily_token_limit' ? { id: 'ai-daily-token-limit' } : {})} value={value} inputMode="numeric" autoComplete="off" disabled={disabled} onChange={(event) => update(key, event.target.value)} />}
            {key !== 'ai.proxy_id' && <>
            <p className="text-caption text-text-secondary">{t('web.ai.default_value', { value: defaultValue })}</p>
            <p className="text-caption text-text-secondary">{messageText(definition.recommendation.text)}</p>
            <p className="text-caption text-text-secondary">{messageText(definition.risk)}</p>
            </>}
            {warnings.map((warning) => <p key={warning.code} role="status" className="text-body text-warning">{messageText(warning)}</p>)}
          </div>;
        })}
        {save.isError && <AIErrorNotice error={save.error} />}
        {saved && <p role="status" className="text-body text-positive">{t('web.ai.saved')}</p>}
        <Button type="submit" variant="primary" busy={save.isPending} busyLabel={t('web.confirm_pending')} disabled={!dirty}>{t('web.ai.save')}</Button>
      </form>
    </FormSection>
  );
}

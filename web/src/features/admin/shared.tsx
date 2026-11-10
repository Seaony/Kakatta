import { useEffect, useRef, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { ApiError, isCancellation } from '../../api/errors';
import { apiData, useApi, useApiError, useTimeZone } from '../../api/hooks';
import { apiKey } from '../../api/keys';
import { InlineMessage, CopyButton } from '../../components/ui';
import { useTranslation } from '../../i18n';
import { formatDateTime } from '../../i18n/format';

export function useAdminSettings() {
  const api = useApi();
  return useQuery({ queryKey: apiKey('settings'), queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/settings', { signal })) });
}

export function useAdminTime() {
  const { t, locale } = useTranslation();
  const timezone = useTimeZone();
  return (at: number | null) => at === null || !timezone ? t('admin.unknown') : formatDateTime(at, locale, timezone);
}

export function ErrorNotice({ error }: { error: unknown }) {
  const text = useApiError();
  const { t } = useTranslation();
  if (!error) return null;
  return <InlineMessage tone="danger"><p>{text(error)}</p>{error instanceof ApiError && error.requestID && <p className="break-all text-caption">{t('web.request_id', { request_id: error.requestID })}</p>}</InlineMessage>;
}

export function fieldError(error: string | undefined): { error?: string } {
  return error ? { error } : {};
}

export function usePrivateAction() {
  const apiError = useApiError();
  const queryClient = useQueryClient();
  const active = useRef<AbortController | null>(null);
  const sequence = useRef(0);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [blockedUntil, setBlockedUntil] = useState(0);
  useEffect(() => {
    if (!blockedUntil) return;
    const timer = setTimeout(() => setBlockedUntil(0), Math.max(0, blockedUntil - Date.now()));
    return () => clearTimeout(timer);
  }, [blockedUntil]);
  const cancel = () => {
    sequence.current++;
    active.current?.abort();
    active.current = null;
    setPending(false);
    setError(null);
  };
  useEffect(() => () => { sequence.current++; active.current?.abort(); }, []);
  async function run<T>(request: (signal: AbortSignal) => Promise<T>, success: (value: T) => void) {
    if (active.current || blockedUntil > Date.now()) return;
    const controller = new AbortController();
    const ticket = ++sequence.current;
    active.current = controller;
    setPending(true);
    setError(null);
    try {
      const value = await request(controller.signal);
      if (ticket === sequence.current && !controller.signal.aborted) success(value);
    } catch (failure) {
      if (ticket === sequence.current && !isCancellation(failure)) {
        setError(failure);
        if (failure instanceof ApiError && failure.status === 429) {
          const seconds = failure.reason.args.seconds;
          if (typeof seconds === 'number' && Number.isFinite(seconds) && seconds > 0) setBlockedUntil(Date.now() + Math.min(seconds * 1000, 2_147_483_647));
        }
        if (failure instanceof ApiError && failure.reason.code === 'auth.unauthenticated') {
          void queryClient.resetQueries({ queryKey: apiKey('me') });
        }
      }
    } finally {
      if (ticket === sequence.current) { active.current = null; setPending(false); }
    }
  }
  function field(key: string): string | undefined {
    if (!(error instanceof ApiError)) return undefined;
    const issue = error.fields.find((entry) => entry.key === key);
    return issue ? apiError(new ApiError(error.status, issue.reason)) : undefined;
  }
  return { pending, blocked: blockedUntil > Date.now(), error, run, cancel, field };
}

export function Command({ command, label }: { command: string; label: string }) {
  const { t } = useTranslation();
  return <div className="min-w-0 space-y-sm"><p className="text-body font-medium">{label}</p><div className="flex flex-wrap items-start gap-sm"><pre className="min-w-0 max-w-full overflow-x-auto rounded-control bg-inset p-md text-body"><code>{command}</code></pre><CopyButton text={command} label={t('admin.copy')} copiedLabel={t('admin.copied')} errorLabel={t('admin.copy_failed')} /></div></div>;
}

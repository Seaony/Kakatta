import { useEffect, useRef, useState } from 'react';
import { apiData, useApi, useMessageText } from '../../api/hooks';
import type { Schema } from '../../api/types';
import { isCancellation } from '../../api/errors';
import { Button, InlineMessage } from '../../components/ui';
import { useTranslation } from '../../i18n';
import { AIErrorNotice } from './error-notice';
import { TrialStep } from './trial-step';
import { TrialRecovery } from './trial-recovery';
import { useAuthentication } from '../../app/auth-state';

export function TrialPanel({ providerId, onFinished }: { providerId: number; onFinished(): void }) {
  const api = useApi();
  const authentication = useAuthentication();
  const { t } = useTranslation();
  const messageText = useMessageText();
  const [report, setReport] = useState<Schema<'TrialReport'> | null>(null);
  const [pending, setPending] = useState(false);
  const [cancelled, setCancelled] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const controller = useRef<AbortController | null>(null);
  const generation = useRef(0);
  const mounted = useRef(true);
  const authenticated = useRef(authentication?.authenticated);
  authenticated.current = authentication?.authenticated;
  function stop() {
    const active = controller.current !== null;
    generation.current++; controller.current?.abort(); controller.current = null;
    if (mounted.current) { setPending(false); if (active) setCancelled(true); }
  }
  useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; generation.current++; controller.current?.abort(); controller.current = null; };
  }, []);
  useEffect(() => api.subscribeAuthenticationLost(stop), [api]);
  useEffect(() => { if (authentication?.authenticated === false) stop(); }, [authentication?.authenticated]);
  async function run() {
    if (controller.current || authenticated.current === false) return;
    const active = new AbortController(), ticket = ++generation.current; controller.current = active;
    const isCurrent = () => mounted.current && generation.current === ticket && !active.signal.aborted && authenticated.current !== false;
    setPending(true); setError(null); setCancelled(false); setReport(null);
    try {
      const next = apiData(await api.POST('/api/v1/ai/providers/{provider_id}/test', { params: { path: { provider_id: providerId } }, signal: active.signal }));
      if (isCurrent()) setReport(next);
    } catch (failure) {
      if (isCurrent()) { if (isCancellation(failure)) setCancelled(true); else setError(failure); }
    } finally {
      if (isCurrent()) { controller.current = null; setPending(false); onFinished(); }
    }
  }
  return <div className="min-w-0 space-y-lg" aria-busy={pending}>
    <p className="text-body text-text-secondary">{t('web.ai.test_cost')}</p>
    <div className="flex flex-wrap gap-sm">
      <Button variant="primary" busy={pending} busyLabel={t('web.ai.testing')} onClick={() => { void run(); }}>{t('web.ai.test')}</Button>
      {pending && <Button onClick={stop}>{t('common.cancel')}</Button>}
    </div>
    {pending && <p role="status" className="text-body text-text-secondary">{t('web.ai.testing')}</p>}
    {cancelled && <p role="status" className="text-body text-text-secondary">{t('web.ai.test_cancelled')}</p>}
    {error !== null && <AIErrorNotice error={error} />}
    {report && <>
      <InlineMessage tone={report.passed ? 'positive' : 'warning'}>{t(report.passed ? 'web.ai.test_passed' : 'web.ai.test_partial')}</InlineMessage>
      {report.reason && <p className="text-body text-warning">{messageText(report.reason)}</p>}
      {([['text', report.text], ['vision', report.vision]] as const).map(([kind, step]) => {
        const detail = step.reason?.args.detail;
        return <TrialStep key={kind} title={t(kind === 'text' ? 'web.ai.text_model' : 'web.ai.vision_model')} state={step.state} model={step.model} jsonMode={step.json_mode} thinking={step.thinking} inputTokens={step.input_tokens} outputTokens={step.output_tokens} reasoningTokens={step.reasoning_tokens} durationMS={step.duration_ms} incomplete={step.usage_incomplete}
          reason={<>{step.reason && <p className="text-body text-danger">{messageText(step.reason)}</p>}<TrialRecovery reason={step.reason} />{step.warnings.map((warning, index) => <p key={index} className="text-body text-warning">{messageText(warning)}</p>)}</>}
          details={step.http_status !== null || typeof detail === 'string' ? <>{step.http_status !== null && <p>{t('web.ai.http_status', { status: step.http_status })}</p>}{typeof detail === 'string' && <p>{detail}</p>}</> : undefined} />;
      })}
    </>}
  </div>;
}

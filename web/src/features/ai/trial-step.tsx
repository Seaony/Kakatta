import type { ReactNode } from 'react';
import type { Schema } from '../../api/types';
import { StatusBadge } from '../../components/ui';
import { useTranslation } from '../../i18n';
import { formatInteger } from '../../i18n/format';

export type TrialStepProps = {
  title: string;
  state: Schema<'TrialStepState'>;
  model: string | null;
  jsonMode: Schema<'ModelClientJSONMode'> | null;
  thinking: boolean | null;
  inputTokens: number;
  outputTokens: number;
  reasoningTokens: number | null;
  durationMS: number | null;
  incomplete: boolean;
  reason?: ReactNode;
  details?: ReactNode;
};

export function TrialStep({ title, state, model, jsonMode, thinking, inputTokens, outputTokens, reasoningTokens, durationMS, incomplete, reason, details }: TrialStepProps) {
  const { t, locale } = useTranslation();
  const value = (number: number | null) => number === null ? t('web.ai.unknown') : formatInteger(number, locale);
  const mode = jsonMode === 'schema' ? t('web.ai.mode_schema') : jsonMode === 'object' ? t('web.ai.mode_object') : jsonMode === 'text' ? t('web.ai.mode_text') : t('web.ai.unknown');
  return <section className="min-w-0 space-y-md border-t border-border pt-lg" aria-label={title}>
    <div className="flex flex-wrap items-center gap-sm">
      <h3 className="text-section font-semibold">{title}</h3>
      <StatusBadge tone={state === 'passed' ? 'positive' : state === 'failed' ? 'danger' : 'neutral'}>{t(state === 'passed' ? 'web.ai.step_passed' : state === 'failed' ? 'web.ai.step_failed' : 'web.ai.test_not_run')}</StatusBadge>
    </div>
    {model && <p className="break-all text-body text-text-label">{model}</p>}
    {reason}
    {state !== 'not_run' && <dl className="grid min-w-0 grid-cols-1 gap-x-xl gap-y-sm sm:grid-cols-2">
      {[
        [t('web.ai.json_mode'), mode],
        [t('web.ai.reasoning'), t(thinking === null ? 'web.ai.unknown' : thinking ? 'web.ai.yes' : 'web.ai.no')],
        [t('web.ai.input_tokens'), value(inputTokens)], [t('web.ai.output_tokens'), value(outputTokens)],
        [t('web.ai.total_tokens'), value(inputTokens + outputTokens)],
        [t('web.ai.reasoning_tokens'), value(reasoningTokens)],
        [t('web.ai.duration'), durationMS === null ? t('web.ai.unknown') : t('web.ai.milliseconds', { value: value(durationMS) })],
      ].map(([label, text]) => <div key={label} className="flex min-w-0 flex-wrap justify-between gap-sm"><dt className="text-body text-text-secondary">{label}</dt><dd className="break-all text-body tabular-nums">{text}</dd></div>)}
    </dl>}
    {state !== 'not_run' && <p className="text-caption text-text-secondary">{t('web.ai.reasoning_in_output')}</p>}
    {incomplete && <p className="text-body text-warning">{t('web.ai.usage_incomplete')}</p>}
    {details && <details className="min-w-0"><summary className="min-h-control cursor-pointer py-sm text-body text-text-label">{t('web.ai.details')}</summary><div className="whitespace-pre-wrap break-words text-body">{details}</div></details>}
  </section>;
}

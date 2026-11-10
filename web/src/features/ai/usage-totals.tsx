import { useTranslation } from '../../i18n';
import { formatInteger } from '../../i18n/format';

export type UsageTotalsProps = { inputTokens: number; outputTokens: number; reasoningTokens: number | null };

export function UsageTotals({ inputTokens, outputTokens, reasoningTokens }: UsageTotalsProps) {
  const { t, locale } = useTranslation();
  return <div className="space-y-md">
    <dl className="grid min-w-0 grid-cols-2 gap-lg sm:grid-cols-4">
      {[
        [t('web.ai.total_tokens'), formatInteger(inputTokens + outputTokens, locale)],
        [t('web.ai.input_tokens'), formatInteger(inputTokens, locale)],
        [t('web.ai.output_tokens'), formatInteger(outputTokens, locale)],
        [t('web.ai.reasoning_tokens'), reasoningTokens === null ? t('web.ai.unknown') : formatInteger(reasoningTokens, locale)],
      ].map(([label, value]) => <div key={label} className="min-w-0 space-y-xs"><dt className="text-body text-text-secondary">{label}</dt><dd className="break-words text-section font-semibold tabular-nums">{value}</dd></div>)}
    </dl>
    <p className="text-caption text-text-secondary">{t('web.ai.reasoning_in_output')}</p>
  </div>;
}

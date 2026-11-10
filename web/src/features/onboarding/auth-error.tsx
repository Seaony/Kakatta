import { useEffect } from 'react';
import type { RefObject } from 'react';
import { ApiError } from '../../api/errors';
import { useApiError, useMessageText } from '../../api/hooks';
import { InlineMessage } from '../../components/ui';
import { useTranslation } from '../../i18n';

export function useAuthFieldError(error: unknown) {
  const message = useMessageText();
  return (key: string): { error?: string } => {
    if (!(error instanceof ApiError)) return {};
    const text = error.fields.filter((field) => field.key === key).map((field) => message(field.reason)).join('\n');
    return text ? { error: text } : {};
  };
}

export function useAuthFieldFocus(error: unknown, form: RefObject<HTMLFormElement | null>) {
  useEffect(() => {
    if (!(error instanceof ApiError)) return;
    const first = error.fields[0];
    const control = first ? form.current?.elements.namedItem(first.key) : null;
    if (control instanceof HTMLElement) control.focus();
  }, [error, form]);
}

export function AuthError({ error }: { error: unknown }) {
  const { t } = useTranslation();
  const errorText = useApiError();
  const message = useMessageText();
  if (!error) return null;
  return (
    <InlineMessage tone="danger">
      <p>{errorText(error)}</p>
      {error instanceof ApiError && error.fields.length > 0 && (
        <ul className="mt-sm list-inside list-disc space-y-xs">
          {error.fields.map((field, index) => <li key={`${field.key}-${index}`}>{message(field.reason)}</li>)}
        </ul>
      )}
      {error instanceof ApiError && error.requestID && (
        <p className="mt-sm break-all text-caption">{t('web.request_id', { request_id: error.requestID })}</p>
      )}
    </InlineMessage>
  );
}

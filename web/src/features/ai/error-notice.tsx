import { ApiError } from '../../api/errors';
import { useApiError } from '../../api/hooks';
import { InlineMessage } from '../../components/ui';
import { useTranslation } from '../../i18n';

export function useAIErrorDescription() {
  const text = useApiError();
  const { t } = useTranslation();
  return (error: unknown): string => {
    const message = text(error);
    return error instanceof ApiError && error.requestID ? `${message} (${t('web.request_id', { request_id: error.requestID })})` : message;
  };
}

export function AIErrorNotice({ error }: { error: unknown }) {
  const text = useApiError();
  const { t } = useTranslation();
  return <InlineMessage tone="danger">
    <p>{text(error)}</p>
    {error instanceof ApiError && error.requestID && <p className="break-all text-caption">{t('web.request_id', { request_id: error.requestID })}</p>}
  </InlineMessage>;
}

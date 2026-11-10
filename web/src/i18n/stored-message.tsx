import { StatusBadge } from '../components/ui/status-badge';
import { formatMessage, useTranslation, type Locale, type Message } from './index';

export function formatStoredMessage(message: Message, locale: Locale) {
  try {
    return { text: formatMessage(message, locale), legacy: false };
  } catch {
    return { text: message.code, legacy: true };
  }
}

export function StoredMessage({ message, renderText }: { message: Message; renderText?: (text: string) => string }) {
  const { locale, t } = useTranslation();
  const formatted = formatStoredMessage(message, locale);
  const text = renderText ? renderText(formatted.text) : formatted.text;
  if (!formatted.legacy) return text;
  return <><span className="break-words">{text}</span><StatusBadge className="mx-xs">{t('web.legacy_message')}</StatusBadge></>;
}

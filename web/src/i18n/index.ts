import { createElement, useCallback, useEffect, useSyncExternalStore } from 'react';
import type { ReactNode } from 'react';
import { createInstance } from 'i18next';
import type { i18n } from 'i18next';
import { I18nextProvider, initReactI18next, useTranslation as useReactTranslation } from 'react-i18next';
import zhCN from '../../../internal/locales/zh-CN.json';
import en from '../../../internal/locales/en.json';
import ja from '../../../internal/locales/ja.json';
import type { Locale } from './locale';

export { selectLocale } from './locale';
export type { Locale, LocalePreference } from './locale';
export type I18nInstance = i18n;
type DictionaryKey = keyof typeof zhCN;
type PluralKey = { [K in DictionaryKey]: K extends `${infer Base}_other` ? Base : never }[DictionaryKey];
export type TranslationKey = DictionaryKey | PluralKey;
export type MessageArgs = Readonly<Record<string, unknown>>;
export type Message = { code: string; args: MessageArgs };

const dictionaries: Readonly<Record<Locale, Readonly<Record<string, string>>>> = Object.freeze({
  'zh-CN': Object.freeze({ ...zhCN }),
  en: Object.freeze({ ...en }),
  ja: Object.freeze({ ...ja }),
});
const placeholder = /\{([A-Za-z_][A-Za-z0-9_]*)\}/g;
const namedRisks = new Set(['analysis.risk_confirmed', 'analysis.risk_penalty', 'analysis.risk_uncertain', 'analysis.risk_unknown']);

export class TranslationError extends Error {
  constructor(readonly kind: 'unsupported_locale' | 'unknown_code' | 'missing_argument' | 'invalid_count') {
    super('翻译文案不可用');
    this.name = 'TranslationError';
  }
}

function dictionary(locale: Locale) {
  if (!Object.hasOwn(dictionaries, locale)) throw new TranslationError('unsupported_locale');
  return dictionaries[locale];
}

export function formatMessage(message: Message, locale: Locale): string {
  const messages = dictionary(locale);
  let args = message.args;
  if (namedRisks.has(message.code) && typeof args.id === 'string' && typeof args.name === 'string') {
    const nameKey = `analysis.risk_name.${args.id}`;
    if (Object.hasOwn(messages, nameKey)) args = { ...args, name: messages[nameKey] };
  }
  let code = message.code;
  if (Object.hasOwn(messages, `${code}_other`)) {
    const count = args.count;
    if (typeof count !== 'number' || !Number.isFinite(count)) throw new TranslationError('invalid_count');
    code += count === 1 ? '_one' : '_other';
  }
  const template = Object.hasOwn(messages, code) ? messages[code] : undefined;
  if (typeof template !== 'string') throw new TranslationError('unknown_code');
  // 只替换原模板，参数中的花括号不能再次变成模板。
  return template.replace(placeholder, (_token, name: string) => {
    if (!Object.hasOwn(args, name) || args[name] === null || args[name] === undefined) {
      throw new TranslationError('missing_argument');
    }
    return String(args[name]);
  });
}

export function createI18n(locale: Locale = 'zh-CN'): I18nInstance {
  dictionary(locale);
  const instance = createInstance();
  void instance.use(initReactI18next).init({
    lng: locale,
    supportedLngs: ['zh-CN', 'en', 'ja'],
    fallbackLng: false,
    load: 'currentOnly',
    initAsync: false,
    keySeparator: false,
    nsSeparator: false,
    returnNull: false,
    returnObjects: false,
    saveMissing: false,
    resources: {
      'zh-CN': { translation: { ...dictionaries['zh-CN'] } },
      en: { translation: { ...dictionaries.en } },
      ja: { translation: { ...dictionaries.ja } },
    },
    interpolation: { prefix: '{', suffix: '}', escapeValue: false, skipOnVariables: true },
    react: { useSuspense: false },
    parseMissingKeyHandler: () => { throw new TranslationError('unknown_code'); },
    missingInterpolationHandler: () => { throw new TranslationError('missing_argument'); },
  });
  return instance;
}

function instanceLocale(instance: I18nInstance): Locale {
  const locale = instance.resolvedLanguage ?? instance.language;
  if (locale !== 'zh-CN' && locale !== 'en' && locale !== 'ja') throw new TranslationError('unsupported_locale');
  return locale;
}

export async function setLocale(instance: I18nInstance, locale: Locale): Promise<void> {
  dictionary(locale);
  await instance.changeLanguage(locale);
}

export function I18nProvider({ instance, children }: { instance: I18nInstance; children: ReactNode }) {
  const subscribe = useCallback((changed: () => void) => {
    instance.on('languageChanged', changed);
    return () => instance.off('languageChanged', changed);
  }, [instance]);
  const snapshot = useCallback(() => instanceLocale(instance), [instance]);
  const locale = useSyncExternalStore(subscribe, snapshot, snapshot);
  useEffect(() => {
    const root = document.documentElement;
    const previous = root.lang;
    root.lang = locale;
    return () => { root.lang = previous; };
  }, [locale]);
  return createElement(I18nextProvider, { i18n: instance }, children);
}

export function useTranslation() {
  const { i18n: instance, ready } = useReactTranslation();
  const locale = instanceLocale(instance);
  const t = useCallback((key: TranslationKey, args: MessageArgs = {}) => formatMessage({ code: key, args }, locale), [locale]);
  const change = useCallback((next: Locale) => setLocale(instance, next), [instance]);
  return { t, locale, setLocale: change, i18n: instance, ready };
}

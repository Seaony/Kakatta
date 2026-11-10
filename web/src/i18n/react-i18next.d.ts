import 'i18next';
import type zhCN from '../../../internal/locales/zh-CN.json';

declare module 'i18next' {
  interface CustomTypeOptions {
    defaultNS: 'translation';
    keySeparator: false;
    nsSeparator: false;
    returnNull: false;
    strictKeyChecks: true;
    resources: { translation: typeof zhCN };
  }
}

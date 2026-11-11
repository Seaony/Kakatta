import { describe, expect, test } from 'bun:test';
import { render, within } from '@testing-library/react';
import { createI18n, formatMessage, I18nProvider, type Locale } from '../../i18n';
import { SettingField } from './setting-field';
import { definitions } from './fixtures.test';
import { makeDraft, settingOwner } from './model';
import type { JsonValue, Message } from './model';

function field(key: string, locale: Locale, value?: JsonValue, warnings: Message[] = []) {
  const definition = definitions.find((item) => item.key === key)!;
  const draft = makeDraft(definition, value ?? definition.default)!;
  return render(<I18nProvider instance={createI18n(locale)}><SettingField definition={definition} draft={draft} source="stored" errors={[]} warnings={warnings} disabled={false} onChange={() => {}} /></I18nProvider>);
}

for (const locale of ['zh-CN', 'en', 'ja'] as const) {
  describe(`${locale}设置说明和警告`, () => {
    test.each(['general.language', 'general.appearance', 'general.timezone', 'general.public_url'])('%s只保留一段用途说明', (key) => {
      const view = field(key, locale);
      const definition = definitions.find((item) => item.key === key)!;
      expect(within(view.container).getByText(formatMessage(definition.why, locale))).toBeTruthy();
      expect(view.container.querySelector('dl') === null).toBe(true);
      view.unmount();
    });
    test.each(definitions.filter((item) => ['integer', 'range', 'quiet_hours'].includes(item.kind) && ['general', 'limits'].includes(settingOwner(item.key) ?? '')).map((item) => item.key))('%s保留数值和时段的四项说明', (key) => {
      const view = field(key, locale);
      expect(view.container.querySelectorAll('dt')).toHaveLength(4);
      view.unmount();
    });
    test.each([
      ['crawl.account_min_gap_seconds', 1, { min: 120 }],
      ['crawl.account_daily_search_visits', 500, { max: 200 }],
      ['crawl.detail_stay_seconds', { min: 0.5, max: 1 }, { min: 3 }],
    ] as const)('%s已保存超出范围时只显示一次警告', (key, value, args) => {
      const view = field(key, locale, value, [{ code: 'settings.outside_recommendation', args }]);
      expect(within(view.container).getAllByText(formatMessage({ code: 'settings.outside_recommendation', args: {} }, locale))).toHaveLength(1);
      view.unmount();
    });
  });
}

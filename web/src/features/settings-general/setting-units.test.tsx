import { expect, test } from 'bun:test';
import { render, screen, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ApiProvider } from '../../api/hooks';
import { createApiClient } from '../../api/client';
import { createI18n, I18nProvider, type Locale } from '../../i18n';
import { SettingField } from './setting-field';
import { AISettingsPanel } from '../ai/settings-panel';
import { OverrideFields } from '../tasks/fields';
import { definitions, snapshot } from './fixtures.test';
import { makeDraft, type Definition } from './model';

const expected = {
  'zh-CN': ['30 分钟', '180 秒', '30', '24 小时', '10–15 秒', '50–130 毫秒', '14 天', '500 MB'],
  en: ['30 minutes', '180 seconds', '30', '24 hours', '10–15 seconds', '50–130 milliseconds', '14 days', '500 MB'],
  ja: ['30 分', '180 秒', '30', '24 時間', '10–15 秒', '50–130 ミリ秒', '14 日', '500 MB'],
} as const;
const keys = ['crawl.task_interval_minutes', 'crawl.account_min_gap_seconds', 'crawl.account_hourly_visits', 'crawl.deny_pause_hours', 'crawl.page_wait_seconds', 'crawl.typing_interval_ms', 'retention.run_steps_days', 'cache.image_max_mb'];

function field(definition: Definition, locale: Locale) {
  return render(<I18nProvider instance={createI18n(locale)}><SettingField definition={definition} draft={makeDraft(definition, definition.default)!} source="default" errors={[]} warnings={[]} disabled={false} onChange={() => {}} /></I18nProvider>);
}

for (const locale of ['zh-CN', 'en', 'ja'] as const) {
  test.each(keys.map((key, index) => [key, expected[locale][index]] as const))(`${locale} %s默认单位随数量显示`, (key, value) => {
    const view = field(definitions.find(item => item.key === key)!, locale);
    expect(view.container.querySelector('dd')?.textContent).toBe(value);
    if (key === 'crawl.account_hourly_visits') expect((view.getByRole('textbox') as HTMLInputElement).labels?.[0]?.textContent).toBe(createI18n(locale).t('settings.crawl.account_hourly_visits.label'));
    view.unmount();
  });

  test(`${locale}单个数量和单值范围使用单数，输入标签不绑定默认数量`, () => {
    const scalar = { ...definitions.find(item => item.key === 'crawl.account_min_gap_seconds')!, default: 1 };
    const view = field(scalar, locale);
    expect(view.container.querySelector('dd')?.textContent).toBe(locale === 'en' ? '1 second' : '1 秒');
    const label = createI18n(locale).t('settings.crawl.account_min_gap_seconds.label');
    expect(within(view.container).getByRole('textbox', { name: `${label} (${locale === 'en' ? 'seconds' : '秒'})` })).toBeTruthy();
    view.unmount();
    const range = field({ ...definitions.find(item => item.key === 'crawl.page_wait_seconds')!, default: { min: 1, max: 1 } }, locale);
    expect(range.container.querySelector('dd')?.textContent).toBe(locale === 'en' ? '1–1 second' : '1–1 秒');
    range.unmount();
  });

  test(`${locale}任务覆盖沿用正确默认单位，不显示泛称数量单位`, () => {
    const view = render(<I18nProvider instance={createI18n(locale)}><OverrideFields value={{}} onChange={() => {}} definitions={definitions} globals={snapshot().values} disabled={false} errorFor={() => undefined} /></I18nProvider>);
    const values = [...view.container.querySelectorAll('dd')].map(node => node.textContent);
    expect(values).toContain(locale === 'en' ? '2–5 seconds' : '2–5 秒');
    expect(values).toContain('5');
    expect(values.some(value => /\bcount\b|数量|\d+ 個/.test(value ?? ''))).toBe(false);
    view.unmount();
  });

  test(`${locale}模型设置默认数量不带count，超时和token按数量显示`, async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
    const api = createApiClient({ fetch: async request => {
      const path = new URL(request.url).pathname;
      if (path.endsWith('/definitions')) return Response.json(definitions);
      if (path.endsWith('/proxies')) return Response.json({ items: [], next_cursor: null, total: 0 });
      return Response.json(snapshot());
    } });
    const i18n = createI18n(locale);
    const view = render(<I18nProvider instance={i18n}><QueryClientProvider client={client}><ApiProvider api={api}><AISettingsPanel /></ApiProvider></QueryClientProvider></I18nProvider>);
    try {
      await screen.findByRole('textbox', { name: i18n.t('settings.ai.image_count.label') });
      for (const value of ['10', '4', '2']) expect(screen.getByText(i18n.t('web.ai.default_value', { value }))).toBeTruthy();
      expect(screen.getByText(i18n.t('web.ai.default_value', { value: locale === 'en' ? '90 seconds' : '90 秒' }))).toBeTruthy();
      expect(screen.getByText(i18n.t('web.ai.default_value', { value: locale === 'en' ? '0 tokens' : locale === 'ja' ? '0 トークン' : '0 token' }))).toBeTruthy();
    } finally { view.unmount(); client.clear(); }
  });
}

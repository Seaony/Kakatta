import { expect, test } from 'bun:test';
import { act, render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ApiProvider } from '../../api/hooks';
import { createApiClient, type ApiFetch } from '../../api/client';
import { createI18n, formatMessage, I18nProvider } from '../../i18n';
import { definitions, snapshot } from '../settings-general/fixtures.test';
import { NotificationsPage } from './rules';

for (const locale of ['zh-CN', 'en', 'ja'] as const) {
  test(`${locale}通知选择用简述，数值上限保留推荐和风险`, async () => {
    const fetch: ApiFetch = async (request) => {
      const path = new URL(request.url).pathname;
      if (path === '/api/v1/settings') return Response.json(snapshot());
      if (path === '/api/v1/settings/definitions') return Response.json(definitions);
      if (path === '/api/v1/channels') return Response.json([]);
      throw new Error(`未声明的测试请求 ${path}`);
    };
    const root = createRootRoute({ component: () => <NotificationsPage /> });
    const router = createRouter({ routeTree: root, history: createMemoryHistory({ initialEntries: ['/'] }) });
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
    await act(async () => { await router.load(); });
    const view = render(<I18nProvider instance={createI18n(locale)}><ApiProvider api={createApiClient({ fetch })}><QueryClientProvider client={queryClient}><RouterProvider router={router} /></QueryClientProvider></ApiProvider></I18nProvider>);
    const i18n = createI18n(locale);
    await screen.findByRole('textbox', { name: i18n.t('settings.notify.max_per_run.label') });
    for (const key of ['notify.link_target', 'notify.system_channel_ids']) {
      const definition = definitions.find((item) => item.key === key)!;
      expect(screen.getByText(formatMessage(definition.why, locale))).toBeTruthy();
      expect(screen.queryAllByText(formatMessage(definition.recommendation.text, locale))).toHaveLength(0);
      expect(screen.queryAllByText(formatMessage(definition.risk, locale))).toHaveLength(0);
    }
    const maximum = definitions.find((item) => item.key === 'notify.max_per_run')!;
    const hint = [maximum.why, maximum.recommendation.text, maximum.risk].map((message) => formatMessage(message, locale)).join(' ');
    expect(screen.getByText(hint)).toBeTruthy();
    view.unmount(); queryClient.clear();
  });
}

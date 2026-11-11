import { expect, test } from 'bun:test';
import { act, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ApiProvider } from '../../api/hooks';
import { createApiClient } from '../../api/client';
import type { Schema } from '../../api/types';
import { createI18n, formatMessage, I18nProvider } from '../../i18n';
import { PromptsPage } from './pages';

const documents: Schema<'PromptView'>[] = (['analysis', 'title_screen', 'profile', 'market'] as const).map((kind) => ({
  kind, content: `${kind} 原始模板`, default: '内置原文', system_suffix: '固定系统说明原文',
  customized: false, required_keys: [], schema: null, asset_version: 1, updated_at: null,
}));

for (const locale of ['zh-CN', 'en', 'ja'] as const) {
  test.each(['pointer', 'keyboard'] as const)(`${locale} 提示词未保存时%s切标签，取消关闭弹窗并保留原标签与草稿`, async (input) => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    const writes: string[] = [];
    const api = createApiClient({ fetch: async (request) => {
      if (request.method !== 'GET') writes.push(request.method);
      return Response.json(new URL(request.url).pathname.endsWith('/settings')
        ? { values: { 'general.timezone': 'UTC' }, sources: {}, warnings: [] } : documents);
    } });
    const view = render(<I18nProvider instance={createI18n(locale)}><QueryClientProvider client={client}>
      <ApiProvider api={api}><PromptsPage /></ApiProvider>
    </QueryClientProvider></I18nProvider>);
    const t = (code: string) => formatMessage({ code, args: {} }, locale);
    try {
      const editor = await screen.findByRole<HTMLTextAreaElement>('textbox', { name: t('web.ai.prompt_content') });
      const user = userEvent.setup();
      await user.type(editor, ' ');
      const original = screen.getByRole('tab', { name: t('web.ai.prompt_analysis') });
      const target = screen.getByRole('tab', { name: t('web.ai.prompt_title_screen') });
      if (input === 'pointer') await user.click(target);
      else {
        await act(async () => { original.focus(); });
        await user.keyboard('[ArrowRight]');
      }
      const dialog = await screen.findByRole('alertdialog', { name: t('web.ai.discard_title') });
      await user.click(within(dialog).getByRole('button', { name: t('common.cancel') }));
      await act(async () => { await new Promise((done) => setTimeout(done, 30)); });
      expect(screen.queryByRole('alertdialog') === null).toBe(true);
      await waitFor(() => expect(document.activeElement === original).toBe(true));
      expect(original.getAttribute('aria-selected')).toBe('true');
      expect(target.getAttribute('aria-selected')).toBe('false');
      expect(screen.getByRole('textbox', { name: t('web.ai.prompt_content') }) === editor).toBe(true);
      expect(editor.value).toBe('analysis 原始模板 ');
      expect(writes).toEqual([]);

      await user.click(target);
      const next = await screen.findByRole('alertdialog', { name: t('web.ai.discard_title') });
      await user.click(within(next).getByRole('button', { name: t('web.ai.discard_confirm') }));
      await waitFor(() => expect(document.activeElement === target).toBe(true));
      expect(screen.queryByRole('alertdialog') === null).toBe(true);
      expect(target.getAttribute('aria-selected')).toBe('true');
      expect((screen.getByRole('textbox', { name: t('web.ai.prompt_content') }) as HTMLTextAreaElement).value).toBe('title_screen 原始模板');
      expect(writes).toEqual([]);
      await user.keyboard('[ArrowRight]');
      const following = screen.getByRole('tab', { name: t('web.ai.prompt_profile') });
      await waitFor(() => expect(following.getAttribute('aria-selected')).toBe('true'));
      expect(document.activeElement === following).toBe(true);
      expect(screen.queryByRole('alertdialog') === null).toBe(true);
    } finally { view.unmount(); client.clear(); }
  });
}

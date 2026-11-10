import { describe, expect, mock, test } from 'bun:test';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import { ApiProvider } from '../../api/hooks';
import { createApiClient, type ApiFetch } from '../../api/client';
import type { Schema } from '../../api/types';
import { createI18n, I18nProvider } from '../../i18n';
import { ProviderForm } from './provider-form';
import { PromptEditor } from './prompt-editor';
import { AISettingsPanel } from './settings-panel';
import { aiSettingKeys, type ProviderDraft } from './draft';
import { TrialStep } from './trial-step';
import { AuthenticationContext, type AuthenticationState } from '../../app/auth-state';

function renderLocalized(children: ReactNode, fetch?: ApiFetch) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const rendered = render(<I18nProvider instance={createI18n('zh-CN')}><QueryClientProvider client={queryClient}><ApiProvider api={createApiClient(fetch ? { fetch } : {})}>{children}</ApiProvider></QueryClientProvider></I18nProvider>);
  return { ...rendered, queryClient };
}

const provider: ProviderDraft = { name: '本地模型', preset: 'custom', baseURL: 'http://model.example/v1', visionModel: 'vision-model', textModel: '', enabled: true, keyAction: 'keep', keyValue: '' };

describe('模型配置表单', () => {
  test('已存密钥不会回填，保留与明确移除分开提交', async () => {
    const save = mock((_draft: ProviderDraft) => {});
    renderLocalized(<ProviderForm initial={provider} apiKeySet onSave={save} />);
    expect(screen.queryByLabelText('API Key', { selector: 'input' })).toBeNull();
    const user = userEvent.setup();
    await user.click(screen.getByRole('button', { name: '保存' }));
    expect(save.mock.calls[0]?.[0]).toEqual(provider);
    await user.selectOptions(screen.getByRole('combobox', { name: 'API Key' }), 'remove');
    await user.click(screen.getByRole('button', { name: '保存' }));
    expect(save.mock.calls[1]?.[0].keyAction).toBe('remove');
    expect(save.mock.calls[1]?.[0].keyValue).toBe('');
  });

  test('处理中禁重复保存并保留完整输入', async () => {
    const save = mock(() => {});
    renderLocalized(<ProviderForm initial={provider} pending onSave={save} />);
    const user = userEvent.setup();
    expect(screen.getByRole('button', { name: '正在处理' }).hasAttribute('disabled')).toBe(true);
    await user.click(screen.getByRole('button', { name: '正在处理' }));
    expect(save).not.toHaveBeenCalled();
    expect(screen.getByRole('textbox', { name: '名称' }).getAttribute('value')).toBe('本地模型');
  });

  test('认证失效清空密钥内存，非敏感草稿保留', async () => {
    const state: AuthenticationState = { authenticated: true, user: undefined, complete: () => {}, refresh: () => {} };
    const instance = createI18n('zh-CN');
    const save = mock(() => {});
    const content = (authentication: AuthenticationState) => <I18nProvider instance={instance}><AuthenticationContext.Provider value={authentication}><ProviderForm initial={provider} onSave={save} /></AuthenticationContext.Provider></I18nProvider>;
    const { rerender } = render(content(state));
    const user = userEvent.setup();
    await user.type(screen.getByLabelText('API Key', { selector: 'input' }), 'temporary-secret');
    await user.type(screen.getByRole('textbox', { name: '名称' }), '修改');
    rerender(content({ ...state, authenticated: false }));
    await waitFor(() => expect((screen.getByLabelText('API Key', { selector: 'input' }) as HTMLInputElement).value).toBe(''));
    expect((screen.getByRole('textbox', { name: '名称' }) as HTMLInputElement).value).toBe('本地模型修改');
  });
});

describe('模板编辑', () => {
  test('保存校验真实必需键，失败保留草稿，再次保存需用户操作', async () => {
    const save = mock(async (_content: string): Promise<string> => { throw new Error('offline'); });
    const restore = mock(async () => '"checks" "adjust" "summary"');
    renderLocalized(<PromptEditor kind="analysis" content={'"checks" "adjust" "summary"'} customized requiredKeys={['checks', 'adjust', 'summary']} onSave={save} onRestore={restore} error="暂时无法保存" />);
    const user = userEvent.setup();
    const editor = screen.getByRole('textbox', { name: '模板内容' });
    await user.clear(editor); await user.type(editor, '缺少格式');
    await user.click(screen.getByRole('button', { name: '保存' }));
    expect(save).not.toHaveBeenCalled();
    expect(screen.getAllByRole('alert').some((element) => element.textContent?.includes('checks'))).toBe(true);
    await user.clear(editor); await user.type(editor, '"checks" "adjust" "summary" 新内容');
    await user.click(screen.getByRole('button', { name: '保存' }));
    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    expect((editor as HTMLTextAreaElement).value).toContain('新内容');
    expect(restore).not.toHaveBeenCalled();
  });
});

describe('样例试分析结果', () => {
  test('思考用量包含在输出，不重复加入合计，未返回与零分开', () => {
    renderLocalized(<TrialStep title="看图步骤" state="passed" model="vision" jsonMode="object" thinking inputTokens={100} outputTokens={80} reasoningTokens={60} durationMS={0} incomplete={false} />);
    expect(screen.getByText('180')).toBeTruthy();
    expect(screen.queryByText('240')).toBeNull();
    expect(screen.getByText(/^0\s*毫秒$/)).toBeTruthy();
  });
  test('英文界面把缺失用量与真实零值分开显示', () => {
    render(<I18nProvider instance={createI18n('en')}><TrialStep title="Vision step" state="passed" model="vision" jsonMode="text" thinking={false} inputTokens={0} outputTokens={0} reasoningTokens={null} durationMS={null} incomplete={false} /></I18nProvider>);
    expect(screen.getAllByText('0')).toHaveLength(3);
    expect(screen.getAllByText('Not reported')).toHaveLength(2);
    expect(screen.getByText('No')).toBeTruthy();
  });
});

function settings(): Schema<'SettingsSnapshot'> {
  const sources: Schema<'SettingsSnapshot'>['sources'] = {};
  for (const key of aiSettingKeys) sources[key] = 'default';
  return { values: { 'ai.max_items_per_run': 10, 'ai.image_count': 4, 'ai.daily_token_limit': 0, 'ai.proxy_id': null, 'ai.max_concurrent_requests': 2, 'ai.request_timeout_seconds': 90 }, sources, warnings: [] };
}
function definitions(): Schema<'SettingDefinition'>[] {
  const values = settings().values;
  return aiSettingKeys.map((key) => ({ key, group: 'ai', kind: key === 'ai.proxy_id' ? 'nullable_id' : 'integer', default: values[key] ?? null, unit: '', options: [], recommendation: { min: null, max: null, text: { code: `settings.${key}.recommendation`, args: {} } }, label: { code: `settings.${key}.label`, args: {} }, why: { code: `settings.${key}.why`, args: {} }, risk: { code: `settings.${key}.risk`, args: {} }, task_override: false }));
}

describe('真实设置接口接线', () => {
  test('五张图片可保存，只提交修改项并保留服务端推荐警告', async () => {
    const patches: unknown[] = [];
    const fetch: ApiFetch = async (request) => {
      const path = new URL(request.url).pathname;
      if (path.endsWith('/definitions')) return Response.json(definitions());
      if (path.endsWith('/proxies')) return Response.json({ items: [], next_cursor: null, total: 0 });
      if (request.method === 'PATCH') {
        patches.push(await request.json());
        const next = settings(); next.values['ai.image_count'] = 5;
        next.warnings = [{ key: 'ai.image_count', reason: { code: 'settings.outside_recommendation', args: { min: 1, max: 4 } } }];
        return Response.json(next);
      }
      return Response.json(settings());
    };
    const { queryClient } = renderLocalized(<AISettingsPanel />, fetch);
    await screen.findByRole('button', { name: '保存' });
    const field = screen.getByRole('textbox', { name: '每件商品发送的图片数' });
    const user = userEvent.setup(); await user.clear(field); await user.type(field, '5');
    await user.click(screen.getByRole('button', { name: '保存' }));
    await screen.findByText('已保存');
    expect(patches).toEqual([{ values: { 'ai.image_count': 5 } }]);
    expect(queryClient.getQueryData<Schema<'SettingsSnapshot'>>(['api', 'settings'])?.values['ai.image_count']).toBe(5);
  });

  test('服务端保存失败保留输入，不自动重发', async () => {
    let writes = 0;
    const fetch: ApiFetch = async (request) => {
      const path = new URL(request.url).pathname;
      if (path.endsWith('/definitions')) return Response.json(definitions());
      if (path.endsWith('/proxies')) return Response.json({ items: [], next_cursor: null, total: 0 });
      if (request.method === 'PATCH') {
        writes++;
        return Response.json({ error: { code: 'web.request_failed', args: {} }, fields: [], request_id: 'local-test' }, { status: 503 });
      }
      return Response.json(settings());
    };
    renderLocalized(<AISettingsPanel />, fetch);
    const field = await screen.findByRole('textbox', { name: '每件商品发送的图片数' });
    const user = userEvent.setup(); await user.clear(field); await user.type(field, '6');
    await user.click(screen.getByRole('button', { name: '保存' }));
    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('请求未完成'));
    expect((field as HTMLInputElement).value).toBe('6');
    expect(writes).toBe(1);
    expect(screen.queryByText('已保存')).toBeNull();
  });
});

import { expect, mock, test } from 'bun:test';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ApiProvider } from '../../api/hooks';
import { createApiClient } from '../../api/client';
import { createI18n, I18nProvider, type Locale } from '../../i18n';
import type { ProviderDraft } from './draft';
import { ProviderForm } from './provider-form';

const expectedAddresses = [
  ['openai', 'https://api.openai.com/v1'],
  ['deepseek', 'https://api.deepseek.com'],
  ['modelscope', 'https://api-inference.modelscope.cn/v1'],
  ['siliconflow', 'https://api.siliconflow.cn/v1'],
  ['openrouter', 'https://openrouter.ai/api/v1'],
  ['aliyun', 'https://dashscope.aliyuncs.com/compatible-mode/v1'],
  ['anthropic', 'https://api.anthropic.com/v1'],
  ['ollama', 'http://host.docker.internal:11434/v1'],
] as const;

const initial: ProviderDraft = {
  name: '已有配置', preset: 'custom', baseURL: 'https://custom.example/v1',
  visionModel: 'existing-vision', textModel: 'existing-text', enabled: false,
  keyAction: 'replace', keyValue: 'existing-secret',
};

function mount(draft = initial, locale: Locale = 'zh-CN') {
  const save = mock((_draft: ProviderDraft) => {});
  const fetch = mock(async (_request: Request) => { throw new Error('不应自动请求'); });
  const result = render(<I18nProvider instance={createI18n(locale)}><ApiProvider api={createApiClient({ fetch })}><ProviderForm initial={draft} apiKeySet onSave={save} /></ApiProvider></I18nProvider>);
  return { ...result, save, fetch };
}

test('用户选择八个预设只预填冻结地址，模型密钥及其他配置保持原值且不自动请求', async () => {
  const { save, fetch } = mount();
  const user = userEvent.setup();
  const preset = screen.getByRole('combobox', { name: '预设' });
  const address = screen.getByRole('textbox', { name: '接口地址' }) as HTMLInputElement;
  for (const [value, expected] of expectedAddresses) {
    await user.selectOptions(preset, value);
    expect(address.value).toBe(expected);
  }
  expect(save).not.toHaveBeenCalled();
  expect(fetch).not.toHaveBeenCalled();
  await user.click(screen.getByRole('button', { name: '保存' }));
  expect(save).toHaveBeenCalledWith({ ...initial, preset: 'ollama', baseURL: 'http://host.docker.internal:11434/v1' });
  expect(fetch).not.toHaveBeenCalled();
});

test('预填地址可以继续手改，切自定义保留手填值，再明确选预设才重新预填', async () => {
  mount();
  const user = userEvent.setup();
  const preset = screen.getByRole('combobox', { name: '预设' });
  const address = screen.getByRole('textbox', { name: '接口地址' }) as HTMLInputElement;
  await user.selectOptions(preset, 'openai');
  await user.clear(address); await user.type(address, 'https://edited.example/v1');
  expect(address.value).toBe('https://edited.example/v1');
  await user.selectOptions(preset, 'custom');
  expect(address.value).toBe('https://edited.example/v1');
  await user.selectOptions(preset, 'openai');
  expect(address.value).toBe('https://api.openai.com/v1');
});

test('编辑挂载所有既有预设均保留其自定义地址及完整配置', async () => {
  const user = userEvent.setup();
  for (const [preset] of expectedAddresses) {
    const draft = { ...initial, preset };
    const { unmount, save, fetch } = mount(draft);
    expect((screen.getByRole('textbox', { name: '接口地址' }) as HTMLInputElement).value).toBe(initial.baseURL);
    await user.click(screen.getByRole('button', { name: '保存' }));
    expect(save).toHaveBeenCalledWith(draft);
    expect(fetch).not.toHaveBeenCalled();
    unmount();
  }
});

test('特定预设显示地域和容器条件，Anthropic 只链接官方兼容文档', async () => {
  const { fetch } = mount(initial, 'en');
  const user = userEvent.setup();
  const preset = screen.getByRole('combobox', { name: 'Preset' });
  await user.selectOptions(preset, 'aliyun');
  expect(screen.getByText(/Beijing.*pay-as-you-go/)).toBeTruthy();
  await user.selectOptions(preset, 'anthropic');
  expect(screen.getByText(/workspace-scoped/)).toBeTruthy();
  const documentation = screen.getByRole('link', { name: /Anthropic.*documentation/ });
  expect(documentation.getAttribute('href')).toBe('https://platform.claude.com/docs/en/cli-sdks-libraries/libraries/openai-sdk');
  await user.selectOptions(preset, 'ollama');
  expect(screen.queryByRole('link', { name: /Anthropic.*documentation/ })).toBeNull();
  expect(screen.getByText(/OLLAMA_HOST=0\.0\.0\.0/)).toBeTruthy();
  expect(screen.getByText(/bundled Compose includes a host-gateway mapping/)).toBeTruthy();
  expect(screen.getByText(/allow access from the container/)).toBeTruthy();
  expect(screen.getByText(/Local Ollama.*no API key/)).toBeTruthy();
  expect(fetch).not.toHaveBeenCalled();
});

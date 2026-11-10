import { expect, test } from 'bun:test';
import { createApiClient } from './client';
import type { components } from './schema.gen';

test('实际客户端保留required default和递归JSON设置输入类型', async () => {
  const definition: components['schemas']['SettingDefinition'] = {
    key: 'crawl.quiet_hours', group: 'crawl', kind: 'quiet_hours',
    default: { enabled: true, start: '01:00', end: '07:30' }, unit: '', options: [],
    recommendation: { min: null, max: null, text: { code: 'common.loading', args: {} } },
    label: { code: 'common.loading', args: {} }, why: { code: 'common.loading', args: {} }, risk: { code: 'common.loading', args: {} }, task_override: false,
  };
  let saved: unknown;
  const api = createApiClient({ fetch: async (request) => {
    if (request.method === 'GET') return Response.json([definition]);
    saved = await request.json();
    return Response.json({ values: {}, sources: {}, warnings: [] });
  } });
  const read = await api.GET('/api/v1/settings/definitions');
  if (read.data === undefined) throw new Error('设置定义响应缺少数据');
  const definitions: components['schemas']['SettingDefinition'][] = read.data;
  expect(definitions[0]?.default).toEqual(definition.default);
  const savedResponse = await api.PATCH('/api/v1/settings', { body: { values: {
    'general.language': 'en',
    'security.trusted_proxies': ['127.0.0.1/8'],
    'crawl.quiet_hours': { enabled: true, start: '01:00', end: '07:30' },
    'ai.proxy_id': null,
  } } });
  if (savedResponse.data === undefined) throw new Error('保存设置响应缺少数据');
  const result: components['schemas']['SettingsSnapshot'] = savedResponse.data;
  expect(result.warnings).toEqual([]);
  expect(saved).toEqual({ values: {
    'general.language': 'en', 'security.trusted_proxies': ['127.0.0.1/8'],
    'crawl.quiet_hours': { enabled: true, start: '01:00', end: '07:30' }, 'ai.proxy_id': null,
  } });
});

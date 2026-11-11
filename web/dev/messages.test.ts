import { expect, test } from 'bun:test';
import { checkLocales } from '../scripts/check-locales';
import { messages, phaseText } from './messages';

test('开发面板三语键和参数完整，日语各阶段都有人可读说明', () => {
  expect(() => checkLocales(messages['zh-CN'], messages.en, messages.ja)).not.toThrow();
  for (const phase of ['starting', 'building', 'reloading', 'reload_failed', 'shutdown_blocked', 'stopping', 'running', 'error']) {
    expect(phaseText(phase, messages.ja)).not.toContain(phase);
  }
});

import { readFile } from 'node:fs/promises';

export function checkLocales(chinese: unknown, english: unknown): void {
  const validate = (value: unknown): Record<string, string> => {
    if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('字典必须是平铺对象。');
    for (const text of Object.values(value)) if (typeof text !== 'string') throw new Error('字典值必须是文字。');
    return value as Record<string, string>;
  };
  const zh = validate(chinese), en = validate(english);
  const keys = Object.keys(zh).sort();
  if (JSON.stringify(keys) !== JSON.stringify(Object.keys(en).sort())) throw new Error('两种语言的键不一致。');
  const parameters = (text: string) => [...new Set([...text.matchAll(/\{([A-Za-z_][A-Za-z0-9_]*)\}/g)].map((match) => match[1]))].sort();
  for (const key of keys) {
    if (JSON.stringify(parameters(zh[key]!)) !== JSON.stringify(parameters(en[key]!))) throw new Error('两种语言的参数不一致：' + key);
    if (key.endsWith('_one') && !(key.slice(0, -4) + '_other' in zh)) throw new Error('复数键缺少 _other：' + key);
    if (key.endsWith('_other') && !(key.slice(0, -6) + '_one' in zh)) throw new Error('复数键缺少 _one：' + key);
  }
}

if (import.meta.main) {
  const [zh, en] = await Promise.all(['zh-CN', 'en'].map(async (locale) => JSON.parse(await readFile(new URL(`../../internal/locales/${locale}.json`, import.meta.url), 'utf8')) as unknown));
  checkLocales(zh, en);
}

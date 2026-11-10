import { expect, test } from 'bun:test';
import { generateApi } from './generated';

test('标准文件字段生成 Blob，备份密码仍是必填字符串', async () => {
  const source = await generateApi();
  expect(source.match(/TaskImportUpload: \{[^}]*\bfile: (\w+);/)?.[1]).toBe('Blob');
  expect(source.match(/BackupRestoreUpload: \{[^}]*\bfile: (\w+);[^}]*\bpassword: (\w+);/)?.slice(1)).toEqual(['Blob', 'string']);
  expect(source.match(/"text\/csv": (\w+);/)?.[1]).toBe('Blob');
  expect(source.match(/"application\/zip": (\w+);/)?.[1]).toBe('Blob');
});

import { expect, test } from 'bun:test';
import { createAppQueryClient } from './query-client';

test('查询和 mutation 不会在失败后隐式重复执行', async () => {
  const client = createAppQueryClient();
  let reads = 0, writes = 0;
  try {
    await expect(client.fetchQuery({ queryKey: ['test'], queryFn: () => { reads++; throw new Error('read'); } })).rejects.toThrow('read');
    const mutation = client.getMutationCache().build(client, { mutationFn: async () => { writes++; throw new Error('write'); } });
    await expect(mutation.execute(undefined)).rejects.toThrow('write');
    expect(reads).toBe(1);
    expect(writes).toBe(1);
    expect(client.getDefaultOptions().queries?.refetchOnWindowFocus).toBe(false);
  } finally { client.clear(); }
});

test('取消 Query 会取消 queryFn 收到的 signal', async () => {
  const client = createAppQueryClient();
  let signal: AbortSignal | undefined;
  const pending = client.fetchQuery({
    queryKey: ['cancel'],
    queryFn: ({ signal: value }) => {
      signal = value;
      return new Promise(() => {});
    },
  });
  const rejected = pending.catch((error: unknown) => error);
  await client.cancelQueries({ queryKey: ['cancel'] });
  expect(await rejected).toBeInstanceOf(Error);
  expect(signal?.aborted).toBe(true);
  client.clear();
});

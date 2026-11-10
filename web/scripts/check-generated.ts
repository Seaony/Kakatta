import { readFile } from 'node:fs/promises';
import { apiOutput, generateApi, generateRoutes, routeOutput } from './generated';

const results = await Promise.all([
  { file: apiOutput, generate: generateApi },
  { file: routeOutput, generate: generateRoutes },
].map(async ({ file, generate }) => ({
  file,
  matches: await readFile(file, 'utf8') === await generate(),
})));

for (const result of results) {
  if (!result.matches) throw new Error('生成文件已过期，请重新生成：' + result.file);
}

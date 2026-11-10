import { mkdir, writeFile } from 'node:fs/promises';
import { dirname } from 'node:path';
import { apiOutput, generateApi } from './generated';

await mkdir(dirname(apiOutput), { recursive: true });
await writeFile(apiOutput, await generateApi());

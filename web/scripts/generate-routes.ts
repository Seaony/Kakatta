import { writeFile } from 'node:fs/promises';
import { generateRoutes, routeOutput } from './generated';

await writeFile(routeOutput, await generateRoutes());

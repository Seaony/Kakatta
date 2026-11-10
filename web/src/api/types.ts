import type { components, operations } from './schema.gen';

export type Schema<Name extends keyof components['schemas']> = components['schemas'][Name];

export type RequestQuery<Name extends keyof operations> = operations[Name] extends {
  parameters: { query?: infer Query };
} ? NonNullable<Query> : never;

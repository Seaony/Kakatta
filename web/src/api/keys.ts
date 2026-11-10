export function apiKey(resource: string, ...segments: readonly unknown[]) {
  return ['api', resource, ...segments] as const;
}

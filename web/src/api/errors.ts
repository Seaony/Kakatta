import type { components } from './schema.gen';

export type Reason = components['schemas']['LocalizedMessage'];
export type FieldError = components['schemas']['FieldIssue'];

export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly reason: Reason,
    readonly fields: FieldError[] = [],
    readonly requestID = '',
  ) {
    super(reason.code);
    this.name = 'ApiError';
  }
}

function object(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function reason(value: unknown): value is Reason {
  return object(value) && typeof value.code === 'string' && object(value.args);
}

export async function responseError(response: Response): Promise<ApiError> {
  let body: unknown;
  try { body = await response.json(); } catch { body = null; }
  if (
    object(body) && reason(body.error) && typeof body.request_id === 'string' &&
    Array.isArray(body.fields) && body.fields.every((field: unknown) => object(field) && typeof field.key === 'string' && reason(field.reason))
  ) {
    return new ApiError(response.status, body.error, body.fields as FieldError[], body.request_id);
  }
  return new ApiError(response.status, { code: 'web.invalid_response', args: {} });
}

export function isCancellation(error: unknown): boolean {
  return error instanceof Error && error.name === 'AbortError';
}

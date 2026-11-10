import type { ReactNode } from "react";

export type FieldText = { label: string; hint?: string | undefined; error?: string | undefined };

export const inputClass =
  "block min-h-control w-full min-w-0 rounded-control border border-border bg-inset px-md py-sm text-input text-text-primary placeholder:text-text-secondary disabled:cursor-not-allowed disabled:opacity-50 aria-[invalid=true]:border-danger";

export function fieldDescription(id: string, hint?: string, error?: string, existing?: string) {
  return [existing, hint && `${id}-hint`, error && `${id}-error`].filter(Boolean).join(" ") || undefined;
}

export function FieldMessages({ id, hint, error }: { id: string; hint?: string | undefined; error?: string | undefined }) {
  return (
    <>
      {hint && <p id={`${id}-hint`} className="break-words text-body text-text-secondary">{hint}</p>}
      {error && <p id={`${id}-error`} role="alert" className="break-words text-body text-danger">{error}</p>}
    </>
  );
}

export function FieldFrame({ id, label, hint, error, children }: FieldText & { id: string; children: ReactNode }) {
  return (
    <div className="min-w-0 space-y-sm">
      <label htmlFor={id} className="block break-words text-body font-medium text-text-primary">{label}</label>
      {children}
      <FieldMessages id={id} {...(hint ? { hint } : {})} {...(error ? { error } : {})} />
    </div>
  );
}

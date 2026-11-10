import { useId, type ComponentPropsWithRef, type ReactNode } from "react";
import { classes, focusClass } from "./styles";

type FieldText = { label: string; hint?: string | undefined; error?: string | undefined };
export type TextFieldProps = ComponentPropsWithRef<"input"> & FieldText;
export type TextAreaFieldProps = ComponentPropsWithRef<"textarea"> & FieldText;

const fieldClass =
  "block min-h-control w-full min-w-0 rounded-control border border-border bg-inset px-md py-sm text-input text-text-primary placeholder:text-text-secondary disabled:cursor-not-allowed disabled:opacity-50 aria-[invalid=true]:border-danger";

function FieldFrame({
  id,
  label,
  hint,
  error,
  children,
}: FieldText & { id: string; children: ReactNode }) {
  return (
    <div className="min-w-0 space-y-sm">
      <label htmlFor={id} className="block text-body font-medium text-text-primary">
        {label}
      </label>
      {children}
      {hint && (
        <p id={`${id}-hint`} className="text-body text-text-secondary">
          {hint}
        </p>
      )}
      {error && (
        <p id={`${id}-error`} role="alert" className="text-body text-danger">
          {error}
        </p>
      )}
    </div>
  );
}

function describedBy(id: string, hint?: string, error?: string, existing?: string) {
  return [existing, hint && `${id}-hint`, error && `${id}-error`].filter(Boolean).join(" ") || undefined;
}

export function TextField({
  id: suppliedID,
  label,
  hint,
  error,
  className,
  "aria-describedby": existingDescription,
  "aria-invalid": invalid,
  ...props
}: TextFieldProps) {
  const generatedID = useId();
  const id = suppliedID ?? generatedID;
  return (
    <FieldFrame id={id} label={label} {...(hint ? { hint } : {})} {...(error ? { error } : {})}>
      <input
        {...props}
        id={id}
        aria-describedby={describedBy(id, hint, error, existingDescription)}
        aria-invalid={error ? true : invalid}
        className={classes(fieldClass, focusClass, className)}
      />
    </FieldFrame>
  );
}

export function TextAreaField({
  id: suppliedID,
  label,
  hint,
  error,
  className,
  "aria-describedby": existingDescription,
  "aria-invalid": invalid,
  rows = 3,
  ...props
}: TextAreaFieldProps) {
  const generatedID = useId();
  const id = suppliedID ?? generatedID;
  return (
    <FieldFrame id={id} label={label} {...(hint ? { hint } : {})} {...(error ? { error } : {})}>
      <textarea
        {...props}
        id={id}
        rows={rows}
        aria-describedby={describedBy(id, hint, error, existingDescription)}
        aria-invalid={error ? true : invalid}
        className={classes(fieldClass, "resize-y", focusClass, className)}
      />
    </FieldFrame>
  );
}

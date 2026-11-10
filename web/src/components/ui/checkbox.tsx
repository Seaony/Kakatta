import { useId, type ComponentPropsWithRef, type RefCallback } from "react";
import { FieldMessages, fieldDescription, type FieldText } from "./field-support";
import { classes, focusClass } from "./styles";

export type CheckboxProps = Omit<ComponentPropsWithRef<"input">, "type"> & FieldText & {
  indeterminate?: boolean;
  onCheckedChange?(checked: boolean): void;
};

export function Checkbox({
  id: suppliedID, label, hint, error, indeterminate = false, onCheckedChange, onChange,
  disabled, className, ref, "aria-describedby": existingDescription, "aria-invalid": invalid, ...props
}: CheckboxProps) {
  const generatedID = useId();
  const id = suppliedID ?? generatedID;
  const setRef: RefCallback<HTMLInputElement> = (element) => {
    if (element) element.indeterminate = indeterminate;
    if (typeof ref === "function") return ref(element);
    if (ref) ref.current = element;
  };
  return (
    <div className="min-w-0 space-y-sm">
      <label htmlFor={id} className={classes("flex min-h-control min-w-0 items-center gap-md py-sm text-body text-text-primary", disabled ? "cursor-not-allowed opacity-50" : "cursor-pointer")}>
        <input
          {...props}
          ref={setRef}
          id={id}
          type="checkbox"
          disabled={disabled}
          aria-checked={indeterminate ? "mixed" : undefined}
          aria-describedby={fieldDescription(id, hint, error, existingDescription)}
          aria-invalid={error ? true : invalid}
          className={classes("h-[1.125rem] w-[1.125rem] shrink-0 accent-accent", focusClass, className)}
          onChange={(event) => { onChange?.(event); onCheckedChange?.(event.currentTarget.checked); }}
        />
        <span className="min-w-0 break-words">{label}</span>
      </label>
      <FieldMessages id={id} {...(hint ? { hint } : {})} {...(error ? { error } : {})} />
    </div>
  );
}

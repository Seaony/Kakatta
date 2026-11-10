import { useId, type ComponentPropsWithRef } from "react";
import { FieldFrame, fieldDescription, inputClass, type FieldText } from "./field-support";
import { classes, focusClass } from "./styles";

export type SelectOption = { value: string; label: string; disabled?: boolean };
export type SelectFieldProps = Omit<ComponentPropsWithRef<"select">, "children"> & FieldText & {
  options: readonly SelectOption[];
  placeholder?: string | undefined;
  onValueChange?(value: string): void;
};

export function SelectField({
  id: suppliedID, label, hint, error, options, placeholder, onValueChange, onChange,
  className, "aria-describedby": existingDescription, "aria-invalid": invalid, ...props
}: SelectFieldProps) {
  const generatedID = useId();
  const id = suppliedID ?? generatedID;
  return (
    <FieldFrame id={id} label={label} {...(hint ? { hint } : {})} {...(error ? { error } : {})}>
      <select
        {...props}
        id={id}
        aria-describedby={fieldDescription(id, hint, error, existingDescription)}
        aria-invalid={error ? true : invalid}
        className={classes(inputClass, focusClass, className)}
        onChange={(event) => { onChange?.(event); onValueChange?.(event.currentTarget.value); }}
      >
        {placeholder && <option value="" disabled>{placeholder}</option>}
        {options.map((option) => <option key={option.value} value={option.value} disabled={option.disabled}>{option.label}</option>)}
      </select>
    </FieldFrame>
  );
}

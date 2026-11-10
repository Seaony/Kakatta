import { RadioGroup as RadioGroupPrimitive } from "radix-ui";
import { useId, type ComponentPropsWithoutRef } from "react";
import { FieldMessages, fieldDescription, type FieldText } from "./field-support";
import { classes, focusClass } from "./styles";

export type RadioOption = { value: string; label: string; description?: string; disabled?: boolean };
export type RadioGroupProps = Omit<ComponentPropsWithoutRef<typeof RadioGroupPrimitive.Root>, "children"> & FieldText & {
  options: readonly RadioOption[];
};

export function RadioGroup({
  id: suppliedID, label, hint, error, options, disabled, className, orientation = "vertical",
  "aria-describedby": existingDescription, "aria-invalid": invalid, ...props
}: RadioGroupProps) {
  const generatedID = useId();
  const id = suppliedID ?? generatedID;
  return (
    <div className="min-w-0 space-y-sm">
      <p id={`${id}-label`} className="break-words text-body font-medium text-text-primary">{label}</p>
      <RadioGroupPrimitive.Root
        {...props}
        id={id}
        disabled={disabled}
        orientation={orientation}
        aria-labelledby={`${id}-label`}
        aria-describedby={fieldDescription(id, hint, error, existingDescription)}
        aria-invalid={error ? true : invalid}
        className={classes("min-w-0 gap-sm", orientation === "horizontal" ? "flex flex-wrap" : "grid", className)}
      >
        {options.map((option, index) => {
          const optionID = `${id}-${index}`;
          const isDisabled = disabled || option.disabled;
          return (
            <label key={option.value} htmlFor={optionID} className={classes("flex min-h-control min-w-0 items-start gap-sm rounded-control pr-sm", isDisabled ? "cursor-not-allowed opacity-50" : "cursor-pointer")}>
              <RadioGroupPrimitive.Item
                id={optionID}
                value={option.value}
                disabled={isDisabled}
                className={classes("group inline-flex min-h-control min-w-control shrink-0 items-center justify-center rounded-control enabled:active:scale-[.98] disabled:cursor-not-allowed motion-reduce:transform-none", focusClass)}
              >
                <span aria-hidden="true" className="flex h-[1.125rem] w-[1.125rem] items-center justify-center rounded-[50%] border border-text-secondary bg-inset group-data-[state=checked]:border-accent-text">
                  <RadioGroupPrimitive.Indicator className="block h-[0.625rem] w-[0.625rem] rounded-[50%] bg-accent-text" />
                </span>
              </RadioGroupPrimitive.Item>
              <span className="min-w-0 space-y-xs py-sm">
                <span className="block break-words text-body text-text-primary">{option.label}</span>
                {option.description && <span className="block break-words text-body text-text-secondary">{option.description}</span>}
              </span>
            </label>
          );
        })}
      </RadioGroupPrimitive.Root>
      <FieldMessages id={id} {...(hint ? { hint } : {})} {...(error ? { error } : {})} />
    </div>
  );
}

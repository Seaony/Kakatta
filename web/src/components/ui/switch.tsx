import { Switch as SwitchPrimitive } from "radix-ui";
import { useId, type ComponentPropsWithRef } from "react";
import { FieldMessages, fieldDescription, type FieldText } from "./field-support";
import { classes, focusClass } from "./styles";

export type SwitchProps = Omit<ComponentPropsWithRef<typeof SwitchPrimitive.Root>, "children"> & FieldText;

export function Switch({
  id: suppliedID, label, hint, error, disabled, className,
  "aria-describedby": existingDescription, "aria-invalid": invalid, ...props
}: SwitchProps) {
  const generatedID = useId();
  const id = suppliedID ?? generatedID;
  return (
    <div className="min-w-0 space-y-sm">
      <div className={classes("flex min-w-0 items-start gap-md", disabled && "opacity-50")}>
        <SwitchPrimitive.Root
          {...props}
          id={id}
          disabled={disabled}
          aria-describedby={fieldDescription(id, hint, error, existingDescription)}
          aria-invalid={error ? true : invalid}
          className={classes("group inline-flex min-h-control min-w-control shrink-0 items-center justify-center rounded-control disabled:cursor-not-allowed enabled:active:scale-[.98] motion-reduce:transform-none", focusClass, className)}
        >
          <span aria-hidden="true" className="flex h-[1.5rem] w-[2.75rem] items-center rounded-[999px] bg-control-hover px-[2px] group-data-[state=checked]:bg-accent">
            <SwitchPrimitive.Thumb className="block h-[1.25rem] w-[1.25rem] rounded-[50%] bg-raised data-[state=checked]:translate-x-[1.25rem] data-[state=checked]:bg-on-accent" />
          </span>
        </SwitchPrimitive.Root>
        <label htmlFor={id} className={classes("flex min-h-control min-w-0 items-center break-words py-sm text-body text-text-primary", disabled ? "cursor-not-allowed" : "cursor-pointer")}>{label}</label>
      </div>
      <FieldMessages id={id} {...(hint ? { hint } : {})} {...(error ? { error } : {})} />
    </div>
  );
}

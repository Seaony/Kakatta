import { useId, useState } from "react";
import { Button } from "./button";
import { TextField, type TextFieldProps } from "./text-field";

export type SecretFieldProps = Omit<TextFieldProps, "type"> & { showLabel: string; hideLabel: string };

export function SecretField({ id: suppliedID, showLabel, hideLabel, disabled, ...props }: SecretFieldProps) {
  const generatedID = useId();
  const id = suppliedID ?? generatedID;
  const [visible, setVisible] = useState(false);
  return (
    <div className="min-w-0 space-y-sm">
      <TextField {...props} id={id} type={visible ? "text" : "password"} disabled={disabled} />
      <Button variant="plain" aria-controls={id} aria-pressed={visible} disabled={disabled} onClick={() => setVisible((current) => !current)}>{visible ? hideLabel : showLabel}</Button>
    </div>
  );
}

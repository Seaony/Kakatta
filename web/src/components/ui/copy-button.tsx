import { useEffect, useRef, useState } from "react";
import { Button, type ButtonProps } from "./button";
import { classes, focusClass } from "./styles";

export type CopyButtonProps = Omit<ButtonProps, "children" | "onClick" | "busy" | "busyLabel"> & {
  text: string;
  label: string;
  copiedLabel: string;
  errorLabel: string;
  onCopied?(): void;
};

export function CopyButton({ text, label, copiedLabel, errorLabel, onCopied, ...props }: CopyButtonProps) {
  const [status, setStatus] = useState<"idle" | "copying" | "copied" | "error">("idle");
  const currentText = useRef(text);
  currentText.current = text;
  useEffect(() => setStatus("idle"), [text]);
  async function copy() {
    const copiedText = text;
    setStatus("copying");
    try {
      await navigator.clipboard.writeText(copiedText);
    } catch {
      if (currentText.current === copiedText) setStatus("error");
      return;
    }
    if (currentText.current !== copiedText) return;
    setStatus("copied");
    onCopied?.();
  }
  return (
    <div className="min-w-0 space-y-sm">
      <Button {...props} busy={status === "copying"} onClick={() => { void copy(); }}>{status === "copied" ? copiedLabel : label}</Button>
      {status === "copied" && <span role="status" className="sr-only">{copiedLabel}</span>}
      {status === "error" && <>
        <p role="alert" className="break-words text-body text-danger">{errorLabel}</p>
        <textarea aria-label={errorLabel} readOnly value={text} rows={3} onFocus={(event) => event.currentTarget.select()} className={classes("block min-h-control w-full min-w-0 resize-y rounded-control border border-border bg-inset px-md py-sm text-input text-text-primary", focusClass)} />
      </>}
    </div>
  );
}

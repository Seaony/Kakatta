import type { ComponentPropsWithRef } from "react";
import { classes, focusClass } from "./styles";

export type ButtonProps = ComponentPropsWithRef<"button"> & {
  variant?: "primary" | "secondary" | "plain" | "destructive";
  busy?: boolean;
  busyLabel?: string;
};

const variants = {
  primary: "bg-accent text-on-accent [@media(hover:hover)]:hover:bg-accent/90",
  secondary:
    "border-border bg-raised text-text-primary [@media(hover:hover)]:hover:bg-control-hover",
  plain:
    "bg-transparent text-text-label [@media(hover:hover)]:hover:bg-control-hover",
  destructive:
    "bg-danger text-raised [@media(hover:hover)]:hover:bg-danger/90",
};

export function Button({
  variant = "secondary",
  busy = false,
  busyLabel,
  type = "button",
  disabled,
  className,
  children,
  ...props
}: ButtonProps) {
  return (
    <button
      {...props}
      type={type}
      disabled={disabled || busy}
      aria-busy={busy || undefined}
      className={classes(
        "inline-flex min-h-control min-w-control max-w-full items-center justify-center gap-sm rounded-control border border-transparent px-md py-sm text-body font-medium leading-normal transition-[background-color,color,border-color,transform] duration-[var(--motion-hover)] ease-out enabled:active:scale-[.98] disabled:cursor-not-allowed disabled:opacity-50 motion-reduce:transform-none motion-reduce:transition-none",
        variants[variant],
        focusClass,
        className,
      )}
    >
      {busy && busyLabel ? busyLabel : children}
    </button>
  );
}

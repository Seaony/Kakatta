import type { ComponentPropsWithoutRef } from "react";
import { classes } from "./styles";

export type StatusBadgeProps = Omit<ComponentPropsWithoutRef<"span">, "children"> & {
  tone?: "neutral" | "accent" | "positive" | "info" | "warning" | "danger";
  children: string;
};

const tones = {
  neutral: "bg-control text-text-label",
  accent: "bg-accent-fill text-accent-text",
  positive: "bg-positive-fill text-positive",
  info: "bg-info-fill text-info",
  warning: "bg-warning-fill text-warning",
  danger: "bg-danger-fill text-danger",
};

export function StatusBadge({ tone = "neutral", children, className, ...props }: StatusBadgeProps) {
  return (
    <span
      {...props}
      className={classes(
        "inline-flex max-w-full items-center rounded-minimum px-sm py-xs text-caption font-medium tabular-nums",
        tones[tone],
        className,
      )}
    >
      {children}
    </span>
  );
}

import type { ReactNode } from "react";
import { classes } from "./styles";

export type InlineMessageProps = {
  tone?: "neutral" | "info" | "positive" | "warning" | "danger";
  title?: string;
  children: ReactNode;
  action?: ReactNode;
  className?: string;
};

const tones = {
  neutral: "bg-control text-text-label",
  info: "bg-info-fill text-info",
  positive: "bg-positive-fill text-positive",
  warning: "bg-warning-fill text-warning",
  danger: "bg-danger-fill text-danger",
};

export function InlineMessage({ tone = "neutral", title, children, action, className }: InlineMessageProps) {
  return (
    <div role={tone === "danger" ? "alert" : "status"} className={classes("min-w-0 space-y-sm rounded-banner p-md text-body", tones[tone], className)}>
      {title && <p className="break-words font-medium">{title}</p>}
      <div className="min-w-0 break-words">{children}</div>
      {action && <div className="flex min-w-0 max-w-full flex-wrap items-center gap-sm">{action}</div>}
    </div>
  );
}

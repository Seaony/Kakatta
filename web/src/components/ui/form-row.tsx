import type { ReactNode } from "react";
import { classes } from "./styles";

export type FormRowProps = { label?: ReactNode; description?: ReactNode; htmlFor?: string; children: ReactNode; className?: string };

export function FormRow({ label, description, htmlFor, children, className }: FormRowProps) {
  return (
    <div className={classes("grid min-w-0 gap-md sm:grid-cols-[minmax(0,1fr)_minmax(0,2fr)] sm:items-start sm:gap-xl", className)}>
      {(label || description) && <div className="min-w-0 space-y-sm sm:py-sm">
        {label && (htmlFor ? <label htmlFor={htmlFor} className="block break-words text-body font-medium text-text-primary">{label}</label> : <div className="break-words text-body font-medium text-text-primary">{label}</div>)}
        {description && <div className="break-words text-body text-text-secondary">{description}</div>}
      </div>}
      <div className={classes("min-w-0", !label && !description && "sm:col-span-2")}>{children}</div>
    </div>
  );
}

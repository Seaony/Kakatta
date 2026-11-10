import type { ReactNode } from "react";
import { classes } from "./styles";

export type PageHeaderProps = { title: string; description?: ReactNode; actions?: ReactNode; children?: ReactNode; className?: string };

export function PageHeader({ title, description, actions, children, className }: PageHeaderProps) {
  return (
    <header className={classes("min-w-0 space-y-lg", className)}>
      <div className="flex min-w-0 flex-wrap items-start justify-between gap-lg">
        <div className="min-w-0 space-y-sm">
          <h1 className="break-words text-page font-semibold text-text-primary">{title}</h1>
          {description && <div className="max-w-prose break-words text-body text-text-secondary">{description}</div>}
        </div>
        {actions && <div className="flex min-w-0 max-w-full flex-wrap items-center gap-sm">{actions}</div>}
      </div>
      {children}
    </header>
  );
}

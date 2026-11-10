import { useId, type ReactNode } from "react";
import { classes } from "./styles";

export type FormSectionProps = { title: string; description?: ReactNode; actions?: ReactNode; children: ReactNode; id?: string; className?: string };

export function FormSection({ title, description, actions, children, id: suppliedID, className }: FormSectionProps) {
  const generatedID = useId();
  const id = suppliedID ?? generatedID;
  return (
    <section id={id} aria-labelledby={`${id}-title`} className={classes("min-w-0 space-y-lg", className)}>
      <header className="flex min-w-0 flex-wrap items-start justify-between gap-md">
        <div className="min-w-0 space-y-sm">
          <h2 id={`${id}-title`} className="break-words text-section font-semibold text-text-primary">{title}</h2>
          {description && <div className="max-w-prose break-words text-body text-text-secondary">{description}</div>}
        </div>
        {actions && <div className="flex min-w-0 max-w-full flex-wrap items-center gap-sm">{actions}</div>}
      </header>
      <div className="min-w-0 space-y-lg">{children}</div>
    </section>
  );
}

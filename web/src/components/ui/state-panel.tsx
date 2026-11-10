import type { ReactNode } from "react";
import { Button } from "./button";

export type StatePanelProps =
  | { kind: "pending"; label: string; children?: ReactNode }
  | { kind: "empty"; title: string; description?: string; action?: ReactNode }
  | { kind: "error"; title: string; description?: string; retry?: { label: string; run(): void } };

export function StatePanel(props: StatePanelProps) {
  if (props.kind === "pending") {
    return (
      <section aria-busy="true" aria-label={props.label} className="min-w-0 space-y-md py-xl">
        <p role="status" className="text-body text-text-secondary">{props.label}</p>
        {props.children}
      </section>
    );
  }
  return (
    <section
      aria-label={props.title}
      role={props.kind === "error" ? "alert" : undefined}
      className="min-w-0 space-y-md py-xl"
    >
      <h2 className="text-section font-semibold text-text-primary">{props.title}</h2>
      {props.description && <p className="max-w-prose text-body text-text-secondary">{props.description}</p>}
      {props.kind === "empty" && props.action}
      {props.kind === "error" && props.retry && <Button onClick={props.retry.run}>{props.retry.label}</Button>}
    </section>
  );
}

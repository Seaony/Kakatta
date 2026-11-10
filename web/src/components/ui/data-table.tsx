import type { ReactNode } from "react";
import { classes, focusClass } from "./styles";

export type DataColumn<Row> = {
  key: string;
  header: ReactNode;
  cell(row: Row): ReactNode;
  align?: "left" | "right";
  className?: string;
};
export type DataTableProps<Row> = {
  label: string;
  columns: readonly DataColumn<Row>[];
  rows: readonly Row[];
  rowKey(row: Row): string | number;
  empty: ReactNode;
  busy?: boolean;
  className?: string;
};

export function DataTable<Row>({ label, columns, rows, rowKey, empty, busy = false, className }: DataTableProps<Row>) {
  return (
    <div role="region" aria-label={label} aria-busy={busy || undefined} tabIndex={0} className={classes("min-w-0 max-w-full overflow-x-auto overscroll-x-contain rounded-control", focusClass, className)}>
      <table className="w-full min-w-[32rem] border-collapse text-left text-body text-text-primary">
        <caption className="sr-only">{label}</caption>
        <thead className="border-b border-border text-text-secondary">
          <tr>{columns.map((column) => <th key={column.key} scope="col" className={classes("px-md py-md font-medium", column.align === "right" && "text-right tabular-nums", column.className)}>{column.header}</th>)}</tr>
        </thead>
        <tbody className="divide-y divide-border">
          {rows.map((row) => <tr key={rowKey(row)}>{columns.map((column) => <td key={column.key} className={classes("break-words px-md py-md align-top", column.align === "right" && "text-right tabular-nums", column.className)}>{column.cell(row)}</td>)}</tr>)}
          {rows.length === 0 && <tr><td colSpan={Math.max(columns.length, 1)} className="px-md py-xl text-text-secondary">{empty}</td></tr>}
        </tbody>
      </table>
    </div>
  );
}

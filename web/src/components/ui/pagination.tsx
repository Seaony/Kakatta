import { Button } from "./button";
import { classes } from "./styles";

export type PaginationProps = {
  page: number;
  hasNextPage: boolean;
  onPageChange(page: number): void;
  previousLabel: string;
  nextLabel: string;
  pageLabel: string;
  disabled?: boolean;
  className?: string;
};

export function Pagination({ page, hasNextPage, onPageChange, previousLabel, nextLabel, pageLabel, disabled = false, className }: PaginationProps) {
  return (
    <div className={classes("flex min-w-0 flex-wrap items-center justify-between gap-md", className)}>
      <p role="status" className="min-w-0 break-words text-body tabular-nums text-text-secondary">{pageLabel}</p>
      <div className="flex min-w-0 max-w-full flex-wrap gap-sm">
        <Button disabled={disabled || page <= 1} onClick={() => onPageChange(page - 1)}>{previousLabel}</Button>
        <Button disabled={disabled || !hasNextPage} onClick={() => onPageChange(page + 1)}>{nextLabel}</Button>
      </div>
    </div>
  );
}

export function classes(...values: Array<string | undefined | false>): string {
  return values.filter(Boolean).join(" ");
}

export const focusClass =
  "focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus focus-visible:transition-none focus-visible:enabled:active:scale-100";

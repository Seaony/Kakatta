import { Tabs as TabsPrimitive } from "radix-ui";
import type { ComponentPropsWithoutRef, ReactNode } from "react";
import { classes, focusClass } from "./styles";

export type TabItem = { value: string; label: string; disabled?: boolean; content?: ReactNode };
export type TabsProps = Omit<ComponentPropsWithoutRef<typeof TabsPrimitive.Root>, "children"> & {
  label: string;
  items: readonly TabItem[];
  children?: ReactNode;
};

export function Tabs({ label, items, defaultValue, className, children, ...props }: TabsProps) {
  const initialValue = defaultValue ?? items.find((item) => !item.disabled)?.value;
  return (
    <TabsPrimitive.Root {...props} {...(initialValue !== undefined ? { defaultValue: initialValue } : {})} className={classes("min-w-0 space-y-lg", className)}>
      <TabsPrimitive.List aria-label={label} className="flex min-w-0 max-w-full gap-xs overflow-x-auto border-b border-border pb-xs">
        {items.map((item) => (
          <TabsPrimitive.Trigger key={item.value} value={item.value} disabled={item.disabled} className={classes("min-h-control min-w-control shrink-0 rounded-control px-md py-sm text-body text-text-label enabled:active:scale-[.98] data-[state=active]:bg-control data-[state=active]:font-medium data-[state=active]:text-text-primary disabled:cursor-not-allowed disabled:opacity-50 motion-reduce:transform-none [@media(hover:hover)]:hover:bg-control-hover", focusClass)}>
            {item.label}
          </TabsPrimitive.Trigger>
        ))}
      </TabsPrimitive.List>
      {items.map((item) => <TabsPrimitive.Content key={item.value} value={item.value} className={classes("min-w-0", focusClass)}>{item.content}</TabsPrimitive.Content>)}
      {children}
    </TabsPrimitive.Root>
  );
}

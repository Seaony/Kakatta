import { Popover as PopoverPrimitive } from "radix-ui";
import type { ReactElement, ReactNode } from "react";

export type PopoverProps = {
  open: boolean;
  onOpenChange(open: boolean): void;
  label: string;
  trigger: ReactElement;
  children: ReactNode;
};

export function Popover({ open, onOpenChange, label, trigger, children }: PopoverProps) {
  return (
    <PopoverPrimitive.Root open={open} onOpenChange={onOpenChange}>
      <PopoverPrimitive.Trigger asChild>{trigger}</PopoverPrimitive.Trigger>
      <PopoverPrimitive.Portal>
        <PopoverPrimitive.Content
          aria-label={label}
          sideOffset={8}
          collisionPadding={16}
          className="z-[var(--layer-popover)] max-h-[var(--radix-popover-content-available-height)] w-[var(--popover-width,20rem)] max-w-[var(--radix-popover-content-available-width)] overflow-y-auto overscroll-contain rounded-panel border border-border bg-raised p-lg text-body text-text-primary shadow-[var(--shadow-overlay)]"
        >
          {children}
        </PopoverPrimitive.Content>
      </PopoverPrimitive.Portal>
    </PopoverPrimitive.Root>
  );
}

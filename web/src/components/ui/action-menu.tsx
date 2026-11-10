import { DropdownMenu as DropdownMenuPrimitive } from "radix-ui";
import type { ReactElement } from "react";
import { Button } from "./button";
import { classes, focusClass } from "./styles";

export type ActionMenuItem = { id: string; label: string; onSelect(): void; disabled?: boolean; destructive?: boolean };
export type ActionMenuProps = { label: string; trigger?: ReactElement; items: readonly ActionMenuItem[] };

export function ActionMenu({ label, trigger, items }: ActionMenuProps) {
  return (
    <DropdownMenuPrimitive.Root>
      <DropdownMenuPrimitive.Trigger asChild>{trigger ?? <Button aria-label={label}>{label}</Button>}</DropdownMenuPrimitive.Trigger>
      <DropdownMenuPrimitive.Portal>
        <DropdownMenuPrimitive.Content
          aria-label={label}
          sideOffset={8}
          collisionPadding={16}
          className="z-[var(--layer-popover)] min-w-[12rem] max-w-[var(--radix-dropdown-menu-content-available-width)] max-h-[var(--radix-dropdown-menu-content-available-height)] overflow-y-auto overscroll-contain rounded-panel border border-border bg-raised p-sm text-body text-text-primary shadow-[var(--shadow-overlay)]"
        >
          {items.map((item) => (
            <DropdownMenuPrimitive.Item key={item.id} disabled={item.disabled} onSelect={item.onSelect} className={classes("flex min-h-control min-w-0 cursor-default items-center whitespace-normal break-words rounded-control px-md py-sm data-[highlighted]:bg-control-hover data-[disabled]:cursor-not-allowed data-[disabled]:opacity-50", focusClass, item.destructive && "text-danger")}>
              {item.label}
            </DropdownMenuPrimitive.Item>
          ))}
        </DropdownMenuPrimitive.Content>
      </DropdownMenuPrimitive.Portal>
    </DropdownMenuPrimitive.Root>
  );
}

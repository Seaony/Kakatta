import { AlertDialog as AlertDialogPrimitive, Dialog as DialogPrimitive } from "radix-ui";
import { useRef, type ReactElement, type ReactNode } from "react";
import { Button } from "./button";

export type DialogProps = {
  open: boolean;
  onOpenChange(open: boolean): void;
  title: string;
  description?: string;
  closeLabel: string;
  trigger?: ReactElement;
  children: ReactNode;
};

export type ConfirmDialogProps = {
  open: boolean;
  onOpenChange(open: boolean): void;
  title: string;
  description?: string;
  confirmLabel: string;
  cancelLabel: string;
  onConfirm(): void;
  pending?: boolean;
  error?: string;
  destructive?: boolean;
  trigger?: ReactElement;
  children?: ReactNode;
};

const overlayClass = "fixed inset-0 z-[var(--layer-overlay)] bg-overlay";
const contentClass =
  "fixed inset-lg top-[max(var(--space-lg),env(safe-area-inset-top))] bottom-[max(var(--space-lg),env(safe-area-inset-bottom))] z-[var(--layer-dialog)] flex min-h-0 flex-col gap-lg overflow-hidden rounded-panel border border-border bg-raised p-lg text-text-primary shadow-[var(--shadow-overlay)] sm:inset-auto sm:left-1/2 sm:top-1/2 sm:max-h-[calc(100vh_-_var(--space-lg)_-_var(--space-lg))] sm:w-[calc(100vw_-_var(--space-lg)_-_var(--space-lg))] sm:max-w-[var(--dialog-width,36rem)] sm:-translate-x-1/2 sm:-translate-y-1/2 sm:p-xl supports-[height:100dvh]:sm:max-h-[calc(min(100dvh,100vh)_-_var(--space-lg)_-_var(--space-lg))]";

function useReturnFocus(hasTrigger: boolean) {
  const previousFocus = useRef<HTMLElement | null>(null);
  return {
    onOpenAutoFocus() {
      previousFocus.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    },
    onCloseAutoFocus(event: Event) {
      if (hasTrigger) return;
      event.preventDefault();
      previousFocus.current?.focus();
    },
  };
}

export function Dialog({ open, onOpenChange, title, description, closeLabel, trigger, children }: DialogProps) {
  const focus = useReturnFocus(Boolean(trigger));
  return (
    <DialogPrimitive.Root open={open} onOpenChange={onOpenChange}>
      {trigger && <DialogPrimitive.Trigger asChild>{trigger}</DialogPrimitive.Trigger>}
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay className={overlayClass} />
        <DialogPrimitive.Content
          {...focus}
          {...(!description ? { "aria-describedby": undefined } : {})}
          className={contentClass}
        >
          <header className="flex shrink-0 items-start justify-between gap-md">
            <div className="min-w-0 space-y-sm">
              <DialogPrimitive.Title className="text-section font-semibold">{title}</DialogPrimitive.Title>
              {description && <DialogPrimitive.Description className="text-body text-text-secondary">{description}</DialogPrimitive.Description>}
            </div>
            <DialogPrimitive.Close asChild><Button variant="plain">{closeLabel}</Button></DialogPrimitive.Close>
          </header>
          <div className="min-h-0 overflow-y-auto overscroll-contain">{children}</div>
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  );
}

export function ConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  confirmLabel,
  cancelLabel,
  onConfirm,
  pending = false,
  error,
  destructive = false,
  trigger,
  children,
}: ConfirmDialogProps) {
  const focus = useReturnFocus(Boolean(trigger));
  return (
    <AlertDialogPrimitive.Root open={open} onOpenChange={onOpenChange}>
      {trigger && <AlertDialogPrimitive.Trigger asChild>{trigger}</AlertDialogPrimitive.Trigger>}
      <AlertDialogPrimitive.Portal>
        <AlertDialogPrimitive.Overlay className={overlayClass} />
        <AlertDialogPrimitive.Content
          {...focus}
          {...(!description ? { "aria-describedby": undefined } : {})}
          className={contentClass}
        >
          <div className="min-h-0 space-y-md overflow-y-auto overscroll-contain">
            <AlertDialogPrimitive.Title className="text-section font-semibold">{title}</AlertDialogPrimitive.Title>
            {description && <AlertDialogPrimitive.Description className="text-body text-text-secondary">{description}</AlertDialogPrimitive.Description>}
            {error && <p role="alert" className="text-body text-danger">{error}</p>}
            {children}
          </div>
          <footer className="flex shrink-0 flex-wrap justify-end gap-sm">
            <AlertDialogPrimitive.Cancel asChild><Button>{cancelLabel}</Button></AlertDialogPrimitive.Cancel>
            <Button variant={destructive ? "destructive" : "primary"} busy={pending} onClick={onConfirm}>{confirmLabel}</Button>
          </footer>
        </AlertDialogPrimitive.Content>
      </AlertDialogPrimitive.Portal>
    </AlertDialogPrimitive.Root>
  );
}

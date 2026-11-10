import { createContext, useCallback, useContext, useEffect, useId, useState, type ReactNode } from 'react';
import { useBlocker } from '@tanstack/react-router';
import { ConfirmDialog } from '../components/ui/dialog';
import { useTranslation } from '../i18n';

type Registry = (id: string, dirty: boolean) => void;
const DirtyContext = createContext<Registry | null>(null);
type ConfirmAction = (action: () => void) => void;
const ActionContext = createContext<ConfirmAction>((action) => action());

export function useUnsavedAction(): ConfirmAction {
  return useContext(ActionContext);
}

export function useUnsavedChanges(dirty: boolean): void {
  const register = useContext(DirtyContext);
  const id = useId();
  useEffect(() => { register?.(id, dirty); }, [register, id, dirty]);
  useEffect(() => () => { register?.(id, false); }, [register, id]);
}

export function UnsavedChangesProvider({ children }: { children: ReactNode }) {
  const { t } = useTranslation();
  const [forms, setForms] = useState<ReadonlySet<string>>(() => new Set());
  const [action, setAction] = useState<(() => void) | null>(null);
  const register = useCallback<Registry>((id, dirty) => {
    setForms((previous) => {
      if (previous.has(id) === dirty) return previous;
      const next = new Set(previous);
      if (dirty) next.add(id); else next.delete(id);
      return next;
    });
  }, []);
  const dirty = forms.size > 0;
  const confirmAction = useCallback<ConfirmAction>((next) => {
    if (dirty) setAction(() => next);
    else next();
  }, [dirty]);
  const blocker = useBlocker({ shouldBlockFn: () => dirty, enableBeforeUnload: dirty, withResolver: true });
  return (
    <DirtyContext.Provider value={register}>
      <ActionContext.Provider value={confirmAction}>{children}</ActionContext.Provider>
      <ConfirmDialog
        open={action !== null || blocker.status === 'blocked'}
        onOpenChange={(open) => { if (!open) { setAction(null); if (blocker.status === 'blocked') blocker.reset(); } }}
        title={t('shell.unsaved_title')}
        description={t('shell.unsaved_description')}
        confirmLabel={t('shell.leave_page')}
        cancelLabel={t('common.cancel')}
        onConfirm={() => {
          if (action) { setAction(null); action(); }
          else if (blocker.status === 'blocked') blocker.proceed();
        }}
        destructive
      />
    </DirtyContext.Provider>
  );
}

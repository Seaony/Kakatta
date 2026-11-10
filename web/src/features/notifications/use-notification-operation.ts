import { useEffect, useRef } from 'react';
import { useAuthentication } from '../../app/auth-state';

export function useNotificationOperation() {
  const authenticated = useAuthentication()?.authenticated;
  const latestAuthentication = useRef(authenticated);
  latestAuthentication.current = authenticated;
  const active = useRef<AbortController | null>(null);
  useEffect(() => {
    if (authenticated === false) { active.current?.abort(); active.current = null; }
  }, [authenticated]);
  useEffect(() => () => { active.current?.abort(); active.current = null; }, []);
  return {
    authenticated,
    begin() {
      if (latestAuthentication.current === false) return null;
      active.current?.abort();
      const controller = new AbortController(); active.current = controller; return controller;
    },
    isCurrent(controller: AbortController) {
      return active.current === controller && !controller.signal.aborted && latestAuthentication.current !== false;
    },
  };
}

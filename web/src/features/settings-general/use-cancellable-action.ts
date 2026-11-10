import { useCallback, useEffect, useRef } from 'react';
import { useApi } from '../../api/hooks';
import { useAuthentication } from '../../app/auth-state';

export function useCancellableAction(onCancel: () => void) {
  const api = useApi();
  const authentication = useAuthentication();
  const enabled = authentication?.authenticated !== false;
  const allowed = useRef(enabled);
  allowed.current = enabled;
  const mounted = useRef(true);
  const controller = useRef<AbortController | null>(null);
  const cancelled = useRef(onCancel);
  cancelled.current = onCancel;

  const cancel = useCallback(() => {
    const current = controller.current;
    if (!current) return;
    controller.current = null;
    current.abort();
    if (mounted.current) cancelled.current();
  }, []);

  useEffect(() => api.subscribeAuthenticationLost(cancel), [api, cancel]);
  useEffect(() => { if (!enabled) cancel(); }, [enabled, cancel]);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      const current = controller.current;
      controller.current = null;
      current?.abort();
    };
  }, []);

  const isActive = useCallback(() => mounted.current && allowed.current, []);
  const start = useCallback(() => {
    if (!mounted.current || !allowed.current || controller.current) return null;
    const next = new AbortController();
    controller.current = next;
    return next;
  }, []);
  const isCurrent = useCallback((request: AbortController) => mounted.current && allowed.current && controller.current === request && !request.signal.aborted, []);
  const finish = useCallback((request: AbortController) => {
    if (controller.current !== request) return false;
    controller.current = null;
    return mounted.current;
  }, []);

  return { enabled, start, cancel, isActive, isCurrent, finish };
}

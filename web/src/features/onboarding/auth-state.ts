import { useEffect, useRef, useState } from 'react';
import { ApiError, isCancellation } from '../../api/errors';

export function useCurrentTime(active: boolean) {
  const [now, setNow] = useState(Date.now);
  useEffect(() => {
    setNow(Date.now());
    if (!active) return;
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [active]);
  return now;
}

// 凭据只经过当前请求，不放进 Query 的 mutation 缓存。
export function useCredentialAction() {
  const controller = useRef<AbortController | null>(null);
  const alive = useRef(true);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [uncertain, setUncertain] = useState(false);
  const [retryAt, setRetryAt] = useState(0);
  const now = useCurrentTime(retryAt > Date.now());
  const waitSeconds = Math.max(0, Math.ceil((retryAt - now) / 1000));

  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
      controller.current?.abort();
      controller.current = null;
    };
  }, []);

  async function run<T>(request: (signal: AbortSignal) => Promise<T>): Promise<T | undefined> {
    if (controller.current || Date.now() < retryAt) return;
    const current = new AbortController();
    controller.current = current;
    setPending(true);
    setError(null);
    setUncertain(false);
    try {
      const value = await request(current.signal);
      if (!alive.current || current.signal.aborted || controller.current !== current) return;
      return value;
    } catch (failure) {
      if (!alive.current || current.signal.aborted || isCancellation(failure)) return;
      setError(failure);
      if (failure instanceof ApiError) {
        if (failure.status === 0) setUncertain(true);
        if (failure.status === 429) {
          const seconds = failure.reason.args.seconds;
          if (typeof seconds === 'number' && Number.isFinite(seconds) && seconds >= 0) {
            setRetryAt(Date.now() + Math.ceil(seconds) * 1000);
          }
        }
      }
    } finally {
      if (alive.current && controller.current === current) {
        controller.current = null;
        setPending(false);
      }
    }
  }

  function cancel() {
    controller.current?.abort();
    controller.current = null;
    setPending(false);
    setUncertain(true);
  }

  return { run, pending, error, waitSeconds, uncertain, cancel, clearError: () => setError(null) };
}

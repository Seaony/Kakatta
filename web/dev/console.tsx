import { useEffect, useRef, useState } from 'react';
import { messages, phaseText } from './messages';

export type DevelopmentMode = 'fake' | 'real';
export type FaultMode = 'normal' | 'expired' | 'risk' | 'missing' | 'api_error';
export type DevelopmentStatus = {
  mode: DevelopmentMode;
  phase: string;
  generation: number;
  backend_ready?: boolean;
  scheduler_paused: boolean;
  last_error: string;
  logs: { seq: number; at: number | string; stream: string; text: string }[];
  fixture?: { fault_mode: string };
};
export type DevelopmentFetch = (path: string, init?: RequestInit) => Promise<Response>;
const transport: DevelopmentFetch = (path, init) => fetch(path, init);
const faults: FaultMode[] = ['normal', 'expired', 'risk', 'missing', 'api_error'];

function readStatus(value: unknown, mode: DevelopmentMode): DevelopmentStatus {
  const status = value as Partial<DevelopmentStatus> | null;
  if (!status || status.mode !== mode || typeof status.phase !== 'string'
      || !Number.isInteger(status.generation) || typeof status.scheduler_paused !== 'boolean'
      || (status.backend_ready !== undefined && typeof status.backend_ready !== 'boolean')
      || typeof status.last_error !== 'string' || !Array.isArray(status.logs)
      || status.logs.some((log) => !log || !Number.isInteger(log.seq)
        || !(typeof log.at === 'string' || Number.isSafeInteger(log.at))
        || typeof log.stream !== 'string' || typeof log.text !== 'string')
      || (status.fixture !== undefined && (!status.fixture || typeof status.fixture.fault_mode !== 'string'))) {
    throw new Error('Invalid development status');
  }
  return status as DevelopmentStatus;
}

function backendReady(status: DevelopmentStatus): boolean {
  return status.backend_ready ?? (status.generation > 0
    && !['starting', 'error', 'stopping', 'shutdown_blocked'].includes(status.phase));
}

function useLanguage() {
  const current = () => document.documentElement.lang.startsWith('en') ? 'en' as const : 'zh-CN' as const;
  const [language, setLanguage] = useState(current);
  useEffect(() => {
    const observer = new MutationObserver(() => setLanguage(current()));
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ['lang'] });
    return () => observer.disconnect();
  }, []);
  return language;
}

export function DevelopmentConsole({ mode, fetcher = transport, pollInterval = 1000 }: {
  mode: DevelopmentMode;
  fetcher?: DevelopmentFetch;
  pollInterval?: number;
}) {
  const language = useLanguage();
  const text = messages[language];
  const [open, setOpen] = useState(false);
  const [status, setStatus] = useState<DevelopmentStatus>();
  const [connected, setConnected] = useState(false);
  const [connectionFailed, setConnectionFailed] = useState(false);
  const [controlError, setControlError] = useState('');
  const [pending, setPending] = useState(false);
  const busy = useRef(false);
  const revision = useRef(0);
  const active = useRef(false);
  const requests = useRef(new Set<AbortController>());

  async function refresh() {
    const version = ++revision.current;
    const controller = new AbortController();
    requests.current.add(controller);
    try {
      const response = await fetcher('/__dev/status', { signal: controller.signal, cache: 'no-store', credentials: 'same-origin' });
      if (!response.ok) throw new Error('Status unavailable');
      const next = readStatus(await response.json(), mode);
      if (!active.current || version !== revision.current) return;
      setStatus(next);
      setConnected(true);
      setConnectionFailed(false);
    } catch {
      if (!active.current || version !== revision.current) return;
      setConnected(false);
      setConnectionFailed(true);
    } finally {
      requests.current.delete(controller);
    }
  }

  useEffect(() => {
    active.current = true;
    let stopped = false;
    let timer: ReturnType<typeof setTimeout>;
    async function poll() {
      if (!busy.current) await refresh();
      if (!stopped) timer = setTimeout(poll, pollInterval);
    }
    void poll();
    return () => {
      active.current = false;
      stopped = true;
      revision.current++;
      clearTimeout(timer);
      for (const request of requests.current) request.abort();
      requests.current.clear();
    };
  }, [mode, fetcher, pollInterval]);

  async function control(path: string, body: { paused: boolean } | { mode: FaultMode }) {
    if (busy.current || !connected || !status) return;
    if (path === '/__dev/scheduler' && !backendReady(status)) return;
    busy.current = true;
    revision.current++;
    for (const request of requests.current) request.abort();
    setPending(true);
    setControlError('');
    const controller = new AbortController();
    requests.current.add(controller);
    try {
      const response = await fetcher(path, {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, credentials: 'same-origin',
        body: JSON.stringify(body), signal: controller.signal,
      });
      if (!response.ok) throw new Error(String(response.status));
      await refresh();
    } catch (error) {
      if (active.current) setControlError(error instanceof Error && /^\d{3}$/.test(error.message) ? error.message : 'failed');
    } finally {
      requests.current.delete(controller);
      busy.current = false;
      if (active.current) setPending(false);
    }
  }

  const managerKnown = connected && status !== undefined;
  const known = managerKnown && backendReady(status);
  const scheduler = !known ? text.unknown : status.scheduler_paused ? text.paused : text.running;
  const phase = managerKnown ? phaseText(status.phase, text) : text.unavailable;
  const fault = status?.fixture?.fault_mode;
  const faultText = fault && faults.includes(fault as FaultMode) ? text[fault as FaultMode] : fault;
  return <>
    <div className={`kakatta-dev-banner${mode === 'real' ? ' kakatta-dev-real' : ''}`} role="status">
      <strong>{text.development} · {mode === 'fake' ? text.fake : scheduler}</strong>
      <span>{mode === 'fake' ? scheduler : phase}</span>
    </div>
    <aside className="kakatta-dev-panel" aria-label={text.console}>
      <button type="button" className="kakatta-dev-toggle" aria-expanded={open}
        aria-controls="kakatta-dev-content" aria-label={open ? text.collapse : text.expand}
        onClick={() => setOpen(!open)}>
        <strong>{text.console}</strong><span>{open ? text.collapse : phase}</span>
      </button>
      {open && <div id="kakatta-dev-content" className="kakatta-dev-content">
        {!managerKnown && <p role={connectionFailed ? 'alert' : 'status'} className={connectionFailed ? 'kakatta-dev-error' : ''}>
          {connectionFailed ? text.connectionError : text.connecting}
        </p>}
        <dl className="kakatta-dev-status">
          <div><dt>{text.status}</dt><dd>{phase}</dd></div>
          <div><dt>{text.generation}</dt><dd>{status?.generation ?? text.unavailable}</dd></div>
          <div><dt>{text.scheduler}</dt><dd>{scheduler}</dd></div>
        </dl>
        <button type="button" disabled={!known || pending} aria-busy={pending || undefined}
          onClick={() => void control('/__dev/scheduler', { paused: !status?.scheduler_paused })}>
          {pending ? text.pending : !known ? text.unknown : status.scheduler_paused ? text.resume : text.pause}
        </button>
        {controlError && <p className="kakatta-dev-error" role="alert">
          {text.controlError}{controlError !== 'failed' ? ` ${text.requestStatus} ${controlError}` : ''}
        </p>}
        {status?.last_error && <section aria-label={text.lastError}>
          <h2>{text.lastError}</h2><pre className="kakatta-dev-error">{status.last_error}</pre>
        </section>}
        {status?.fixture && <section aria-label={text.fixture}>
          <h2>{text.fixture}</h2>
          <p>{text.nextRun}</p><p>{text.selectedFault}：{faultText}</p>
          <div className="kakatta-dev-faults">{faults.map((item) => <button key={item} type="button"
            disabled={!managerKnown || pending} aria-pressed={fault === item}
            onClick={() => void control('/__dev/fault', { mode: item })}>{text[item]}</button>)}</div>
        </section>}
        <section aria-label={text.logs}>
          <h2>{text.logs}</h2><p>{text.logLimit}</p>
          {status?.logs.length ? <ol className="kakatta-dev-logs">{status.logs.slice(-100).map((log) => <li key={log.seq}>
            <span>{logTime(log.at, language)} · {log.stream}</span><pre>{log.text}</pre>
          </li>)}</ol> : <p>{text.noLogs}</p>}
        </section>
      </div>}
    </aside>
  </>;
}

function logTime(value: number | string, language: string): string {
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) ? String(value)
    : new Intl.DateTimeFormat(language, { dateStyle: 'short', timeStyle: 'medium' }).format(date);
}

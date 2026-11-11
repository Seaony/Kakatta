import { useEffect } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { useApi } from '../api/hooks';
import { apiKey } from '../api/keys';

type QueryScope = string | readonly [resource: string, ...segments: string[]];

const affectedResources: Readonly<Record<string, readonly QueryScope[]>> = {
  // 详情的行情依赖时区；重分析预检不随设置重发。
  'settings.changed': ['settings', 'me', 'tasks', 'accounts', 'overview', 'ai-usage', 'system-status', 'update-status', ['findings', 'detail']],
  'update.changed': ['update-status'],
  'task.changed': ['tasks', 'findings', 'overview'],
  'account.changed': ['accounts', 'tasks', 'overview', 'system-status'],
  'session.changed': ['accounts', 'tasks', 'system-status'],
  'finding.changed': ['findings', 'listings', 'tasks', 'overview'],
  'run.changed': ['runs', 'tasks', 'accounts', 'overview', 'system-status'],
  'runs.changed': ['runs', 'tasks', 'accounts', 'overview', 'system-status'],
  'market.changed': ['tasks', 'findings', 'overview'],
  'ai.providers.changed': ['ai-providers', 'tasks', 'overview', 'system-status'],
  'ai.usage.changed': ['ai-usage', 'tasks', 'overview', 'system-status'],
  'ai.prompts.changed': ['prompts'],
  'proxy.changed': ['proxies', 'accounts', 'ai-providers'],
  'channel.changed': ['channels', 'deliveries', 'findings', 'overview'],
  'system_event.changed': ['system-events', 'overview', 'system-status'],
  'maintenance.changed': ['system-status', 'overview'],
};

export function useLiveEvents(authenticated: boolean): void {
  const api = useApi();
  const queryClient = useQueryClient();
  useEffect(() => {
    if (!authenticated || typeof EventSource === 'undefined') return;
    let active = true;
    let current: { stream: EventSource; controller: AbortController } | undefined;
    function close() {
      const previous = current;
      current = undefined;
      previous?.controller.abort();
      previous?.stream.close();
    }
    function connect(): boolean {
      if (!active || current) return false;
      const stream = new EventSource('/api/v1/events'), controller = new AbortController();
      const connection = { stream, controller };
      current = connection;
      let identityChecked = false;
      const isCurrent = () => active && current === connection && !controller.signal.aborted;
      for (const [type, resources] of Object.entries(affectedResources)) {
        stream.addEventListener(type, () => {
          if (!isCurrent()) return;
          for (const resource of resources) {
            const queryKey = typeof resource === 'string' ? apiKey(resource) : apiKey(...resource);
            void queryClient.invalidateQueries({ queryKey });
          }
        });
      }
      stream.addEventListener('reset', () => { if (isCurrent()) void queryClient.invalidateQueries({ queryKey: ['api'] }); });
      stream.onopen = () => { if (isCurrent()) identityChecked = false; };
      stream.onerror = () => {
        if (!isCurrent() || identityChecked) return;
        identityChecked = true;
        // 断流本身不能证明登录失效；只读核对后再交统一登录边界。
        void api.GET('/api/v1/me', { signal: controller.signal }).catch(() => {});
      };
      return true;
    }
    const resume = (event: PageTransitionEvent) => {
      // 浏览器缓存的页面不会卸载；恢复时补读离开期间错过的变化。
      if (event.persisted && connect()) void queryClient.invalidateQueries({ queryKey: ['api'] });
    };
    const unsubscribe = api.subscribeAuthenticationLost(() => { active = false; close(); });
    window.addEventListener('pagehide', close);
    window.addEventListener('pageshow', resume);
    connect();
    return () => {
      active = false; unsubscribe();
      window.removeEventListener('pagehide', close);
      window.removeEventListener('pageshow', resume);
      close();
    };
  }, [api, authenticated, queryClient]);
}

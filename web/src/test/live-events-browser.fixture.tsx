import { useState } from 'react';
import { createRoot } from 'react-dom/client';
import { QueryClientProvider, useQuery } from '@tanstack/react-query';
import { createApiClient } from '../api/client';
import { apiData, ApiProvider, useApi } from '../api/hooks';
import { apiKey } from '../api/keys';
import { createAppQueryClient } from '../app/query-client';
import { useLiveEvents } from '../app/live-events';

const documentID = crypto.randomUUID();
Object.assign(window, { liveEventsProbe: { documentID, persisted: false } });
window.addEventListener('pageshow', (event) => {
  Object.assign(window, { liveEventsProbe: { documentID, persisted: event.persisted } });
});

function Fixture() {
  useLiveEvents(true);
  const api = useApi(), [draft, setDraft] = useState('');
  const revision = useQuery({ queryKey: apiKey('settings'), queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/settings', { signal })) });
  return <main><h1>{location.pathname}</h1><p id="revision">{revision.data?.values['probe.revision'] ?? 'loading'}</p>
    <label>草稿<input id="draft" value={draft} onChange={(event) => setDraft(event.target.value)} /></label>
  </main>;
}

createRoot(document.getElementById('root')!).render(<QueryClientProvider client={createAppQueryClient()}><ApiProvider api={createApiClient()}><Fixture /></ApiProvider></QueryClientProvider>);

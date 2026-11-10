import { createFileRoute } from '@tanstack/react-router';
import { AIPage } from '../features/ai';
import { aiSearch } from '../app/search';

function Page() {
  const search = Route.useSearch();
  return <AIPage {...(search.provider_id !== undefined ? { providerId: search.provider_id } : {})} />;
}

export const Route = createFileRoute('/settings/ai')({ validateSearch: aiSearch, component: Page });

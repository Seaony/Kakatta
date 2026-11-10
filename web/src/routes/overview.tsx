import { createFileRoute } from '@tanstack/react-router';
import { OverviewPage } from '../features/overview';
import { overviewSearch } from '../app/search';

function Page() {
  const search = Route.useSearch();
  const navigate = Route.useNavigate();
  return <OverviewPage period={search.period ?? 'today'} onPeriodChange={(period) => { void navigate({ search: { period } }); }} />;
}

export const Route = createFileRoute('/overview')({ validateSearch: overviewSearch, component: Page });

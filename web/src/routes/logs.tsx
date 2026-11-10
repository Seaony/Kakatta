import { createFileRoute } from '@tanstack/react-router';
import { LogsPage } from '../features/logs';
import { logsSearch } from '../app/search';

function Page() {
  const search = Route.useSearch();
  const navigate = Route.useNavigate();
  return <LogsPage search={search} onSearchChange={(next) => { void navigate({ search: next }); }} />;
}

export const Route = createFileRoute('/logs')({ validateSearch: logsSearch, component: Page });

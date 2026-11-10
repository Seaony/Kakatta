import { createFileRoute } from '@tanstack/react-router';
import { ResultsPage } from '../features/results';
import { resultsContextUnchanged, resultsSearch } from '../app/search';

function Page() {
  const search = Route.useSearch();
  const navigate = Route.useNavigate();
  return <ResultsPage search={search} onSearchChange={(next) => { void navigate({ search: next, resetScroll: !resultsContextUnchanged(search, next) }); }} />;
}

export const Route = createFileRoute('/results')({ validateSearch: resultsSearch, component: Page });

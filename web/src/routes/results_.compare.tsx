import { createFileRoute } from '@tanstack/react-router';
import { ComparePage } from '../features/detail-compare';
import { compareSearch, comparisonIds } from '../app/search';

function Page() {
  const search = Route.useSearch();
  return <ComparePage ids={comparisonIds(search.ids)} />;
}

export const Route = createFileRoute('/results_/compare')({ validateSearch: compareSearch, component: Page });

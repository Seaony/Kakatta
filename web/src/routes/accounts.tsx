import { createFileRoute } from '@tanstack/react-router';
import { AccountsPage } from '../features/accounts';
import { accountsSearch } from '../app/search';

function Page() {
  const search = Route.useSearch();
  const navigate = Route.useNavigate();
  return <AccountsPage {...(search.account_id !== undefined ? { accountId: search.account_id } : {})} onAccountChange={(account_id) => { void navigate({ search: { account_id } }); }} />;
}

export const Route = createFileRoute('/accounts')({ validateSearch: accountsSearch, component: Page });

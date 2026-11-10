import { createFileRoute } from '@tanstack/react-router';
import { TasksPage } from '../features/tasks';
import { tasksSearch } from '../app/search';

function Page() {
  const search = Route.useSearch();
  const navigate = Route.useNavigate();
  return <TasksPage search={search} onSearchChange={(next) => { void navigate({ search: next }); }} />;
}

export const Route = createFileRoute('/tasks')({ validateSearch: tasksSearch, component: Page });

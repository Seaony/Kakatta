import { createFileRoute } from '@tanstack/react-router';
import { SetupPage } from '../features/onboarding';
import { useAuthentication } from '../app/auth-state';

function Page() {
  const authentication = useAuthentication();
  return <SetupPage onAuthenticated={(user) => authentication?.complete(user)} />;
}

export const Route = createFileRoute('/setup')({ component: Page });

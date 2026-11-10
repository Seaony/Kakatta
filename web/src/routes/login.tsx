import { createFileRoute } from '@tanstack/react-router';
import { LoginPage } from '../features/onboarding';
import { useAuthentication } from '../app/auth-state';

function Page() {
  const authentication = useAuthentication();
  return <LoginPage onAuthenticated={(user) => authentication?.complete(user)} />;
}

export const Route = createFileRoute('/login')({ component: Page });

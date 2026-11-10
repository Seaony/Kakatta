import { createFileRoute } from '@tanstack/react-router';
import { SecurityPage } from '../features/admin';

export const Route = createFileRoute('/settings/security')({ component: SecurityPage });

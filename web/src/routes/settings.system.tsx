import { createFileRoute } from '@tanstack/react-router';
import { SystemPage } from '../features/admin';

export const Route = createFileRoute('/settings/system')({ component: SystemPage });

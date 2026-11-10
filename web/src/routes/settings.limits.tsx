import { createFileRoute } from '@tanstack/react-router';
import { LimitsPage } from '../features/settings-general';

export const Route = createFileRoute('/settings/limits')({ component: LimitsPage });

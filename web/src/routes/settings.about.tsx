import { createFileRoute } from '@tanstack/react-router';
import { AboutPage } from '../features/admin';

export const Route = createFileRoute('/settings/about')({ component: AboutPage });

import { createFileRoute } from '@tanstack/react-router';
import { BackupPage } from '../features/admin';

export const Route = createFileRoute('/settings/backup')({ component: BackupPage });

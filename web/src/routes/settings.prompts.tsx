import { createFileRoute } from '@tanstack/react-router';
import { PromptsPage } from '../features/ai';

export const Route = createFileRoute('/settings/prompts')({ component: PromptsPage });

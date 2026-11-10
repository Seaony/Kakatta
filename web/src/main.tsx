import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { createApiClient } from './api/client';
import { AppProviders } from './app/providers';
import { createAppQueryClient } from './app/query-client';
import { createAppRouter } from './app/router';
import { selectLocale } from './i18n';
import './styles/base.css';

const queryClient = createAppQueryClient();
const router = createAppRouter({ queryClient, api: createApiClient() });
const container = document.getElementById('root');
if (!container) throw new Error('页面缺少应用入口。');

createRoot(container).render(
  <StrictMode>
    <AppProviders router={router} queryClient={queryClient} initialLocale={selectLocale('system')} initialTheme="system" />
  </StrictMode>,
);

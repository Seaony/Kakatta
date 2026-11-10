// @ts-nocheck

// 路由表由生成器维护，请修改 routes 目录。

import { Route as rootRouteImport } from './routes/__root';
import { Route as IndexRouteImport } from './routes/index';
import { Route as AccountsRouteImport } from './routes/accounts';
import { Route as LoginRouteImport } from './routes/login';
import { Route as LogsRouteImport } from './routes/logs';
import { Route as OnboardingRouteImport } from './routes/onboarding';
import { Route as OverviewRouteImport } from './routes/overview';
import { Route as ResultsRouteImport } from './routes/results';
import { Route as SetupRouteImport } from './routes/setup';
import { Route as TasksRouteImport } from './routes/tasks';
import { Route as ResultsCompareRouteImport } from './routes/results_.compare';
import { Route as SettingsAboutRouteImport } from './routes/settings.about';
import { Route as SettingsAiRouteImport } from './routes/settings.ai';
import { Route as SettingsBackupRouteImport } from './routes/settings.backup';
import { Route as SettingsGeneralRouteImport } from './routes/settings.general';
import { Route as SettingsLimitsRouteImport } from './routes/settings.limits';
import { Route as SettingsNotificationsRouteImport } from './routes/settings.notifications';
import { Route as SettingsPromptsRouteImport } from './routes/settings.prompts';
import { Route as SettingsSecurityRouteImport } from './routes/settings.security';
import { Route as SettingsSystemRouteImport } from './routes/settings.system';

const IndexRoute = IndexRouteImport.update({
  id: '/',
  path: '/',
  getParentRoute: () => rootRouteImport,
} as any);
const AccountsRoute = AccountsRouteImport.update({
  id: '/accounts',
  path: '/accounts',
  getParentRoute: () => rootRouteImport,
} as any);
const LoginRoute = LoginRouteImport.update({
  id: '/login',
  path: '/login',
  getParentRoute: () => rootRouteImport,
} as any);
const LogsRoute = LogsRouteImport.update({
  id: '/logs',
  path: '/logs',
  getParentRoute: () => rootRouteImport,
} as any);
const OnboardingRoute = OnboardingRouteImport.update({
  id: '/onboarding',
  path: '/onboarding',
  getParentRoute: () => rootRouteImport,
} as any);
const OverviewRoute = OverviewRouteImport.update({
  id: '/overview',
  path: '/overview',
  getParentRoute: () => rootRouteImport,
} as any);
const ResultsRoute = ResultsRouteImport.update({
  id: '/results',
  path: '/results',
  getParentRoute: () => rootRouteImport,
} as any);
const SetupRoute = SetupRouteImport.update({
  id: '/setup',
  path: '/setup',
  getParentRoute: () => rootRouteImport,
} as any);
const TasksRoute = TasksRouteImport.update({
  id: '/tasks',
  path: '/tasks',
  getParentRoute: () => rootRouteImport,
} as any);
const ResultsCompareRoute = ResultsCompareRouteImport.update({
  id: '/results_/compare',
  path: '/results/compare',
  getParentRoute: () => rootRouteImport,
} as any);
const SettingsAboutRoute = SettingsAboutRouteImport.update({
  id: '/settings/about',
  path: '/settings/about',
  getParentRoute: () => rootRouteImport,
} as any);
const SettingsAiRoute = SettingsAiRouteImport.update({
  id: '/settings/ai',
  path: '/settings/ai',
  getParentRoute: () => rootRouteImport,
} as any);
const SettingsBackupRoute = SettingsBackupRouteImport.update({
  id: '/settings/backup',
  path: '/settings/backup',
  getParentRoute: () => rootRouteImport,
} as any);
const SettingsGeneralRoute = SettingsGeneralRouteImport.update({
  id: '/settings/general',
  path: '/settings/general',
  getParentRoute: () => rootRouteImport,
} as any);
const SettingsLimitsRoute = SettingsLimitsRouteImport.update({
  id: '/settings/limits',
  path: '/settings/limits',
  getParentRoute: () => rootRouteImport,
} as any);
const SettingsNotificationsRoute = SettingsNotificationsRouteImport.update({
  id: '/settings/notifications',
  path: '/settings/notifications',
  getParentRoute: () => rootRouteImport,
} as any);
const SettingsPromptsRoute = SettingsPromptsRouteImport.update({
  id: '/settings/prompts',
  path: '/settings/prompts',
  getParentRoute: () => rootRouteImport,
} as any);
const SettingsSecurityRoute = SettingsSecurityRouteImport.update({
  id: '/settings/security',
  path: '/settings/security',
  getParentRoute: () => rootRouteImport,
} as any);
const SettingsSystemRoute = SettingsSystemRouteImport.update({
  id: '/settings/system',
  path: '/settings/system',
  getParentRoute: () => rootRouteImport,
} as any);

export interface FileRoutesByFullPath {
  '/': typeof IndexRoute;
  '/accounts': typeof AccountsRoute;
  '/login': typeof LoginRoute;
  '/logs': typeof LogsRoute;
  '/onboarding': typeof OnboardingRoute;
  '/overview': typeof OverviewRoute;
  '/results': typeof ResultsRoute;
  '/setup': typeof SetupRoute;
  '/tasks': typeof TasksRoute;
  '/results/compare': typeof ResultsCompareRoute;
  '/settings/about': typeof SettingsAboutRoute;
  '/settings/ai': typeof SettingsAiRoute;
  '/settings/backup': typeof SettingsBackupRoute;
  '/settings/general': typeof SettingsGeneralRoute;
  '/settings/limits': typeof SettingsLimitsRoute;
  '/settings/notifications': typeof SettingsNotificationsRoute;
  '/settings/prompts': typeof SettingsPromptsRoute;
  '/settings/security': typeof SettingsSecurityRoute;
  '/settings/system': typeof SettingsSystemRoute;
}
export interface FileRoutesByTo {
  '/': typeof IndexRoute;
  '/accounts': typeof AccountsRoute;
  '/login': typeof LoginRoute;
  '/logs': typeof LogsRoute;
  '/onboarding': typeof OnboardingRoute;
  '/overview': typeof OverviewRoute;
  '/results': typeof ResultsRoute;
  '/setup': typeof SetupRoute;
  '/tasks': typeof TasksRoute;
  '/results/compare': typeof ResultsCompareRoute;
  '/settings/about': typeof SettingsAboutRoute;
  '/settings/ai': typeof SettingsAiRoute;
  '/settings/backup': typeof SettingsBackupRoute;
  '/settings/general': typeof SettingsGeneralRoute;
  '/settings/limits': typeof SettingsLimitsRoute;
  '/settings/notifications': typeof SettingsNotificationsRoute;
  '/settings/prompts': typeof SettingsPromptsRoute;
  '/settings/security': typeof SettingsSecurityRoute;
  '/settings/system': typeof SettingsSystemRoute;
}
export interface FileRoutesById {
  __root__: typeof rootRouteImport;
  '/': typeof IndexRoute;
  '/accounts': typeof AccountsRoute;
  '/login': typeof LoginRoute;
  '/logs': typeof LogsRoute;
  '/onboarding': typeof OnboardingRoute;
  '/overview': typeof OverviewRoute;
  '/results': typeof ResultsRoute;
  '/setup': typeof SetupRoute;
  '/tasks': typeof TasksRoute;
  '/results_/compare': typeof ResultsCompareRoute;
  '/settings/about': typeof SettingsAboutRoute;
  '/settings/ai': typeof SettingsAiRoute;
  '/settings/backup': typeof SettingsBackupRoute;
  '/settings/general': typeof SettingsGeneralRoute;
  '/settings/limits': typeof SettingsLimitsRoute;
  '/settings/notifications': typeof SettingsNotificationsRoute;
  '/settings/prompts': typeof SettingsPromptsRoute;
  '/settings/security': typeof SettingsSecurityRoute;
  '/settings/system': typeof SettingsSystemRoute;
}
export interface FileRouteTypes {
  fileRoutesByFullPath: FileRoutesByFullPath;
  fullPaths:
    | '/'
    | '/accounts'
    | '/login'
    | '/logs'
    | '/onboarding'
    | '/overview'
    | '/results'
    | '/setup'
    | '/tasks'
    | '/results/compare'
    | '/settings/about'
    | '/settings/ai'
    | '/settings/backup'
    | '/settings/general'
    | '/settings/limits'
    | '/settings/notifications'
    | '/settings/prompts'
    | '/settings/security'
    | '/settings/system';
  fileRoutesByTo: FileRoutesByTo;
  to:
    | '/'
    | '/accounts'
    | '/login'
    | '/logs'
    | '/onboarding'
    | '/overview'
    | '/results'
    | '/setup'
    | '/tasks'
    | '/results/compare'
    | '/settings/about'
    | '/settings/ai'
    | '/settings/backup'
    | '/settings/general'
    | '/settings/limits'
    | '/settings/notifications'
    | '/settings/prompts'
    | '/settings/security'
    | '/settings/system';
  id:
    | '__root__'
    | '/'
    | '/accounts'
    | '/login'
    | '/logs'
    | '/onboarding'
    | '/overview'
    | '/results'
    | '/setup'
    | '/tasks'
    | '/results_/compare'
    | '/settings/about'
    | '/settings/ai'
    | '/settings/backup'
    | '/settings/general'
    | '/settings/limits'
    | '/settings/notifications'
    | '/settings/prompts'
    | '/settings/security'
    | '/settings/system';
  fileRoutesById: FileRoutesById;
}
export interface RootRouteChildren {
  IndexRoute: typeof IndexRoute;
  AccountsRoute: typeof AccountsRoute;
  LoginRoute: typeof LoginRoute;
  LogsRoute: typeof LogsRoute;
  OnboardingRoute: typeof OnboardingRoute;
  OverviewRoute: typeof OverviewRoute;
  ResultsRoute: typeof ResultsRoute;
  SetupRoute: typeof SetupRoute;
  TasksRoute: typeof TasksRoute;
  ResultsCompareRoute: typeof ResultsCompareRoute;
  SettingsAboutRoute: typeof SettingsAboutRoute;
  SettingsAiRoute: typeof SettingsAiRoute;
  SettingsBackupRoute: typeof SettingsBackupRoute;
  SettingsGeneralRoute: typeof SettingsGeneralRoute;
  SettingsLimitsRoute: typeof SettingsLimitsRoute;
  SettingsNotificationsRoute: typeof SettingsNotificationsRoute;
  SettingsPromptsRoute: typeof SettingsPromptsRoute;
  SettingsSecurityRoute: typeof SettingsSecurityRoute;
  SettingsSystemRoute: typeof SettingsSystemRoute;
}

declare module '@tanstack/react-router' {
  interface FileRoutesByPath {
    '/': {
      id: '/';
      path: '/';
      fullPath: '/';
      preLoaderRoute: typeof IndexRouteImport;
      parentRoute: typeof rootRouteImport;
    };
    '/accounts': {
      id: '/accounts';
      path: '/accounts';
      fullPath: '/accounts';
      preLoaderRoute: typeof AccountsRouteImport;
      parentRoute: typeof rootRouteImport;
    };
    '/login': {
      id: '/login';
      path: '/login';
      fullPath: '/login';
      preLoaderRoute: typeof LoginRouteImport;
      parentRoute: typeof rootRouteImport;
    };
    '/logs': {
      id: '/logs';
      path: '/logs';
      fullPath: '/logs';
      preLoaderRoute: typeof LogsRouteImport;
      parentRoute: typeof rootRouteImport;
    };
    '/onboarding': {
      id: '/onboarding';
      path: '/onboarding';
      fullPath: '/onboarding';
      preLoaderRoute: typeof OnboardingRouteImport;
      parentRoute: typeof rootRouteImport;
    };
    '/overview': {
      id: '/overview';
      path: '/overview';
      fullPath: '/overview';
      preLoaderRoute: typeof OverviewRouteImport;
      parentRoute: typeof rootRouteImport;
    };
    '/results': {
      id: '/results';
      path: '/results';
      fullPath: '/results';
      preLoaderRoute: typeof ResultsRouteImport;
      parentRoute: typeof rootRouteImport;
    };
    '/setup': {
      id: '/setup';
      path: '/setup';
      fullPath: '/setup';
      preLoaderRoute: typeof SetupRouteImport;
      parentRoute: typeof rootRouteImport;
    };
    '/tasks': {
      id: '/tasks';
      path: '/tasks';
      fullPath: '/tasks';
      preLoaderRoute: typeof TasksRouteImport;
      parentRoute: typeof rootRouteImport;
    };
    '/results_/compare': {
      id: '/results_/compare';
      path: '/results/compare';
      fullPath: '/results/compare';
      preLoaderRoute: typeof ResultsCompareRouteImport;
      parentRoute: typeof rootRouteImport;
    };
    '/settings/about': {
      id: '/settings/about';
      path: '/settings/about';
      fullPath: '/settings/about';
      preLoaderRoute: typeof SettingsAboutRouteImport;
      parentRoute: typeof rootRouteImport;
    };
    '/settings/ai': {
      id: '/settings/ai';
      path: '/settings/ai';
      fullPath: '/settings/ai';
      preLoaderRoute: typeof SettingsAiRouteImport;
      parentRoute: typeof rootRouteImport;
    };
    '/settings/backup': {
      id: '/settings/backup';
      path: '/settings/backup';
      fullPath: '/settings/backup';
      preLoaderRoute: typeof SettingsBackupRouteImport;
      parentRoute: typeof rootRouteImport;
    };
    '/settings/general': {
      id: '/settings/general';
      path: '/settings/general';
      fullPath: '/settings/general';
      preLoaderRoute: typeof SettingsGeneralRouteImport;
      parentRoute: typeof rootRouteImport;
    };
    '/settings/limits': {
      id: '/settings/limits';
      path: '/settings/limits';
      fullPath: '/settings/limits';
      preLoaderRoute: typeof SettingsLimitsRouteImport;
      parentRoute: typeof rootRouteImport;
    };
    '/settings/notifications': {
      id: '/settings/notifications';
      path: '/settings/notifications';
      fullPath: '/settings/notifications';
      preLoaderRoute: typeof SettingsNotificationsRouteImport;
      parentRoute: typeof rootRouteImport;
    };
    '/settings/prompts': {
      id: '/settings/prompts';
      path: '/settings/prompts';
      fullPath: '/settings/prompts';
      preLoaderRoute: typeof SettingsPromptsRouteImport;
      parentRoute: typeof rootRouteImport;
    };
    '/settings/security': {
      id: '/settings/security';
      path: '/settings/security';
      fullPath: '/settings/security';
      preLoaderRoute: typeof SettingsSecurityRouteImport;
      parentRoute: typeof rootRouteImport;
    };
    '/settings/system': {
      id: '/settings/system';
      path: '/settings/system';
      fullPath: '/settings/system';
      preLoaderRoute: typeof SettingsSystemRouteImport;
      parentRoute: typeof rootRouteImport;
    };
  }
}

const rootRouteChildren: RootRouteChildren = {
  IndexRoute: IndexRoute,
  AccountsRoute: AccountsRoute,
  LoginRoute: LoginRoute,
  LogsRoute: LogsRoute,
  OnboardingRoute: OnboardingRoute,
  OverviewRoute: OverviewRoute,
  ResultsRoute: ResultsRoute,
  SetupRoute: SetupRoute,
  TasksRoute: TasksRoute,
  ResultsCompareRoute: ResultsCompareRoute,
  SettingsAboutRoute: SettingsAboutRoute,
  SettingsAiRoute: SettingsAiRoute,
  SettingsBackupRoute: SettingsBackupRoute,
  SettingsGeneralRoute: SettingsGeneralRoute,
  SettingsLimitsRoute: SettingsLimitsRoute,
  SettingsNotificationsRoute: SettingsNotificationsRoute,
  SettingsPromptsRoute: SettingsPromptsRoute,
  SettingsSecurityRoute: SettingsSecurityRoute,
  SettingsSystemRoute: SettingsSystemRoute,
};
export const routeTree = rootRouteImport
  ._addFileChildren(rootRouteChildren)
  ._addFileTypes<FileRouteTypes>();

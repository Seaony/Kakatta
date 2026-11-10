import { createContext, useContext } from 'react';
import type { Schema } from '../api/types';

export type AuthenticationState = {
  authenticated: boolean;
  user: Schema<'Me'> | undefined;
  complete(user: Schema<'Me'>): void;
  refresh(): void;
};

export const AuthenticationContext = createContext<AuthenticationState | null>(null);

export function useAuthentication(): AuthenticationState | null {
  return useContext(AuthenticationContext);
}

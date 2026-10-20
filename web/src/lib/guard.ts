import type { User } from './types';

export const LOGIN_PATH = '/login';
export const CHANGE_PASSWORD_PATH = '/change-password';

/** Only follow a `next` target that stays inside this app. */
export function safeNext(next: string | null | undefined): string {
  if (!next || !next.startsWith('/') || next.startsWith('//') || next.startsWith('/\\')) return '/';
  if (next === LOGIN_PATH || next.startsWith(LOGIN_PATH + '?')) return '/';
  return next;
}

export function loginUrl(returnTo: string): string {
  const next = safeNext(returnTo);
  return next === '/' ? LOGIN_PATH : `${LOGIN_PATH}?next=${encodeURIComponent(next)}`;
}

/**
 * Where a visitor must go for the current user state, or null to stay.
 * `path` is the pathname, `search` the query string with its leading `?`.
 */
export function guardRedirect(user: User | null, path: string, search = ''): string | null {
  if (!user) {
    return path === LOGIN_PATH ? null : loginUrl(path + search);
  }
  if (user.must_change_password) {
    return path === CHANGE_PASSWORD_PATH ? null : CHANGE_PASSWORD_PATH;
  }
  if (path === LOGIN_PATH) return safeNext(new URLSearchParams(search).get('next'));
  return null;
}

import { ApiError, client, setAuthHooks, setCsrfToken } from './api';
import type { MeInfo, User } from './types';

type Status = 'unknown' | 'anonymous' | 'authenticated';
type Fetch = typeof fetch;

/** The signed in user. The CSRF token lives in api.ts memory, never in storage. */
export class SessionStore {
  user = $state<User | null>(null);
  status = $state<Status>('unknown');

  private pending: Promise<void> | null = null;

  /** Load the session once per page load (after a reload `me` also returns the CSRF token). */
  ensure(f: Fetch = fetch): Promise<void> {
    if (this.status !== 'unknown') return Promise.resolve();
    this.pending ??= this.refresh(f).finally(() => (this.pending = null));
    return this.pending;
  }

  /** Ask the server who the caller is. A 401 means signed out, other errors bubble up. */
  async refresh(f: Fetch = fetch): Promise<void> {
    try {
      this.accept(await client(f).me());
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) this.expire();
      else throw e;
    }
  }

  async login(email: string, password: string): Promise<User> {
    const session = await client().login(email.trim(), password);
    setCsrfToken(session.csrf_token);
    this.user = session.user;
    this.status = 'authenticated';
    return session.user;
  }

  async logout(): Promise<void> {
    try {
      await client().logout();
    } finally {
      // Even if the call failed, the user asked to leave: drop the local state.
      this.expire();
    }
  }

  async changePassword(current: string, next: string): Promise<void> {
    await client().changePassword(current, next);
    if (this.user) this.user = { ...this.user, must_change_password: false };
  }

  /** The server says there is no valid session. */
  expire(): void {
    setCsrfToken('');
    this.user = null;
    this.status = 'anonymous';
  }

  /** The server limited the user to the password form. */
  requirePasswordChange(): void {
    if (this.user) this.user = { ...this.user, must_change_password: true };
  }

  private accept(me: MeInfo): void {
    setCsrfToken(me.csrf_token ?? '');
    this.user = me.user;
    this.status = 'authenticated';
  }
}

export const session = new SessionStore();

// Wire the failures every API call can hit (see api.ts) to the session.
setAuthHooks({
  unauthenticated: () => session.expire(),
  passwordChangeRequired: () => session.requirePasswordChange(),
  csrfInvalid: () => void session.refresh().catch(() => {})
});

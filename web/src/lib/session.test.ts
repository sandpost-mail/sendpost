import { afterEach, describe, expect, it, vi } from 'vitest';
import { setCsrfToken } from './api';
import { SessionStore } from './session.svelte';
import type { User } from './types';

const user: User = {
  id: 1,
  email: 'a@example.test',
  name: 'A',
  is_admin: true,
  must_change_password: false,
  created_at: '2026-01-01T00:00:00Z',
  disabled_at: null
};

const json = (status: number, body?: unknown) =>
  new Response(body === undefined ? null : JSON.stringify(body), { status });

afterEach(() => {
  vi.unstubAllGlobals();
  setCsrfToken('');
});

describe('SessionStore', () => {
  it('restores the user and CSRF token after a reload', async () => {
    const f = vi.fn().mockResolvedValue(json(200, { user, auth_kind: 'session', csrf_token: 'c1' }));
    vi.stubGlobal('fetch', f);
    const s = new SessionStore();
    await s.ensure();
    expect(s.status).toBe('authenticated');
    expect(s.user?.email).toBe('a@example.test');
    // the next unsafe call carries the token
    f.mockResolvedValueOnce(json(204));
    await s.logout();
    expect(f.mock.calls[1][1].headers['X-CSRF-Token']).toBe('c1');
  });

  it('asks the server once even for parallel loads', async () => {
    const f = vi.fn().mockResolvedValue(json(200, { user, auth_kind: 'session', csrf_token: 'c' }));
    vi.stubGlobal('fetch', f);
    const s = new SessionStore();
    await Promise.all([s.ensure(), s.ensure()]);
    await s.ensure();
    expect(f).toHaveBeenCalledTimes(1);
  });

  it('is anonymous on 401', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(json(401, { error: 'no', code: 'unauthenticated' })));
    const s = new SessionStore();
    await s.ensure();
    expect(s.status).toBe('anonymous');
    expect(s.user).toBeNull();
  });

  it('surfaces other failures from ensure', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(json(500, { error: 'boom' })));
    await expect(new SessionStore().ensure()).rejects.toThrow('boom');
  });

  it('login stores the user and keeps the forced change flag', async () => {
    const forced = { ...user, must_change_password: true };
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(json(200, { user: forced, csrf_token: 'c2', expires_at: 'x' }))
    );
    const s = new SessionStore();
    const got = await s.login(' a@example.test ', 'pw');
    expect(got.must_change_password).toBe(true);
    expect(s.status).toBe('authenticated');
  });

  it('a successful password change clears the flag, logout clears the user', async () => {
    const s = new SessionStore();
    s.user = { ...user, must_change_password: true };
    s.status = 'authenticated';
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(json(204)));
    await s.changePassword('old', 'new-password-123');
    expect(s.user?.must_change_password).toBe(false);
    await s.logout();
    expect(s.user).toBeNull();
    expect(s.status).toBe('anonymous');
  });

  it('forgets the user even if the logout call fails', async () => {
    const s = new SessionStore();
    s.user = user;
    s.status = 'authenticated';
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(json(500, { error: 'boom' })));
    await expect(s.logout()).rejects.toThrow();
    expect(s.user).toBeNull();
  });

  it('requirePasswordChange flags the user', () => {
    const s = new SessionStore();
    s.user = user;
    s.requirePasswordChange();
    expect(s.user?.must_change_password).toBe(true);
  });
});

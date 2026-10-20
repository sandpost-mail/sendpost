import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiError, client, setAuthHooks, setCsrfToken } from './api';

function reply(status: number, body?: unknown): Response {
  return new Response(body === undefined ? null : JSON.stringify(body), { status });
}

afterEach(() => {
  setCsrfToken('');
  setAuthHooks({});
});

describe('api client', () => {
  it('sends cookies and no CSRF header on GET', async () => {
    const f = vi.fn().mockResolvedValue(reply(200, { items: [] }));
    await client(f).listProjects();
    const [url, init] = f.mock.calls[0];
    expect(url).toBe('/_/api/v1/projects');
    expect(init.credentials).toBe('same-origin');
    expect(init.headers).toBeUndefined();
  });

  it('sends the CSRF token and JSON content type on unsafe requests', async () => {
    setCsrfToken('tok-123');
    const f = vi.fn().mockResolvedValue(reply(204));
    await client(f).markRead(1, 2);
    const [, init] = f.mock.calls[0];
    expect(init.method).toBe('POST');
    expect(init.headers).toEqual({ 'Content-Type': 'application/json', 'X-CSRF-Token': 'tok-123' });
  });

  it('exposes status and code of failures', async () => {
    const f = vi.fn().mockResolvedValue(reply(403, { error: 'nope', code: 'csrf_invalid' }));
    const err = await client(f).createGroup('x').catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 403, code: 'csrf_invalid', message: 'nope' });
  });

  it('calls the unauthenticated hook on 401 but not for login or me', async () => {
    const unauthenticated = vi.fn();
    setAuthHooks({ unauthenticated });
    const f = vi.fn().mockResolvedValue(reply(401, { error: 'no', code: 'unauthenticated' }));
    await client(f).listGroups().catch(() => {});
    expect(unauthenticated).toHaveBeenCalledTimes(1);
    await client(f).login('a@b.c', 'x').catch(() => {});
    await client(f).me().catch(() => {});
    expect(unauthenticated).toHaveBeenCalledTimes(1);
  });

  it('calls the password change hook on password_change_required', async () => {
    const passwordChangeRequired = vi.fn();
    setAuthHooks({ passwordChangeRequired });
    const f = vi.fn().mockResolvedValue(reply(403, { error: 'change it', code: 'password_change_required' }));
    await client(f).listProjects().catch(() => {});
    expect(passwordChangeRequired).toHaveBeenCalledTimes(1);
  });

  it('does not treat a plain 403 as a session problem', async () => {
    const hooks = { unauthenticated: vi.fn(), passwordChangeRequired: vi.fn(), csrfInvalid: vi.fn() };
    setAuthHooks(hooks);
    const f = vi.fn().mockResolvedValue(reply(403, { error: 'forbidden' }));
    await client(f).listUsers().catch(() => {});
    expect(hooks.unauthenticated).not.toHaveBeenCalled();
    expect(hooks.passwordChangeRequired).not.toHaveBeenCalled();
    expect(hooks.csrfInvalid).not.toHaveBeenCalled();
  });

  it('builds paged audit and token requests', async () => {
    const f = vi.fn().mockResolvedValue(reply(200, { items: [], next_cursor: '' }));
    await client(f).listProjectAudit(7, 'abc', 25);
    expect(f.mock.calls[0][0]).toBe('/_/api/v1/projects/7/audit?cursor=abc&limit=25');
    const g = vi.fn().mockResolvedValue(reply(201, {}));
    await client(g).createProjectToken(7, { label: 'ci', scopes: ['read'] });
    expect(JSON.parse(g.mock.calls[0][1].body)).toEqual({ label: 'ci', scopes: ['read'] });
  });
});

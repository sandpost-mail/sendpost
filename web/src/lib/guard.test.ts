import { describe, expect, it } from 'vitest';
import { guardRedirect, loginUrl, safeNext } from './guard';
import type { User } from './types';

const user = (over: Partial<User> = {}): User => ({
  id: 1,
  email: 'a@example.test',
  name: '',
  is_admin: false,
  must_change_password: false,
  created_at: '2026-01-01T00:00:00Z',
  disabled_at: null,
  ...over
});

describe('safeNext', () => {
  it('keeps local paths', () => expect(safeNext('/projects/3?x=1')).toBe('/projects/3?x=1'));
  it.each(['//evil.test', 'https://evil.test', '/\\evil', '', null, undefined, '/login'])(
    'rejects %s',
    (next) => expect(safeNext(next)).toBe('/')
  );
});

describe('guardRedirect', () => {
  it('sends anonymous visitors to login and remembers the target', () => {
    expect(guardRedirect(null, '/projects/2', '?q=1')).toBe('/login?next=%2Fprojects%2F2%3Fq%3D1');
    expect(guardRedirect(null, '/')).toBe('/login');
  });
  it('lets anonymous visitors open the login page', () => {
    expect(guardRedirect(null, '/login')).toBeNull();
  });
  it('limits a forced password change to its page', () => {
    const u = user({ must_change_password: true });
    expect(guardRedirect(u, '/projects/1')).toBe('/change-password');
    expect(guardRedirect(u, '/login')).toBe('/change-password');
    expect(guardRedirect(u, '/change-password')).toBeNull();
  });
  it('sends signed in users away from the login page', () => {
    expect(guardRedirect(user(), '/login', '?next=%2Fadmin%2Fusers')).toBe('/admin/users');
    expect(guardRedirect(user(), '/login', '?next=//evil.test')).toBe('/');
    expect(guardRedirect(user(), '/projects/1')).toBeNull();
  });
});

describe('message permalink', () => {
  it('survives the login round trip', () => {
    const link = '/projects/3/messages/42';
    const toLogin = guardRedirect(null, link);
    expect(toLogin).toBe('/login?next=%2Fprojects%2F3%2Fmessages%2F42');
    const search = '?' + toLogin!.split('?')[1];
    expect(guardRedirect(user(), '/login', search)).toBe(link);
  });
});

describe('loginUrl', () => {
  it('omits next for the root', () => expect(loginUrl('/')).toBe('/login'));
});

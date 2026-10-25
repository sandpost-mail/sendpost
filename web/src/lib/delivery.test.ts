import { describe, expect, it } from 'vitest';
import {
  confirmationFor,
  connectionChanged,
  deliveryWarnings,
  hostError,
  isDomainEntry,
  parseAllowlist,
  portError,
  sesPreset,
  statusText
} from './delivery';

const live = { enabled: true };

describe('allowlist text', () => {
  it('splits lines, commas and semicolons and drops blanks', () => {
    expect(parseAllowlist('a@x.test\n b@x.test ,\n\n@corp.example;c@y.test')).toEqual([
      'a@x.test',
      'b@x.test',
      '@corp.example',
      'c@y.test'
    ]);
    expect(parseAllowlist('  \n ')).toEqual([]);
  });

  it('tells domain entries from addresses', () => {
    expect(isDomainEntry('@corp.example')).toBe(true);
    expect(isDomainEntry('corp.example')).toBe(true);
    expect(isDomainEntry('dev@corp.example')).toBe(false);
  });
});

describe('deliveryWarnings', () => {
  const warn = (mode: 'allow' | 'redirect' | 'capture', list: string[], redirect: string, t: { enabled: boolean } | null) =>
    deliveryWarnings({ mode, allowlist: list, redirect_address: redirect }, t);
  const levels = (w: { level: string }[]) => w.map((x) => x.level);

  it('capture only is informational', () => {
    expect(warn('capture', ['@x.test'], '', live)).toEqual([expect.objectContaining({ level: 'info' })]);
  });

  it('says plainly when nothing will be sent', () => {
    expect(warn('allow', ['a@x.test'], '', null).map((w) => w.text).join(' ')).toMatch(/No transport/);
    expect(warn('allow', ['a@x.test'], '', { enabled: false }).map((w) => w.text).join(' ')).toMatch(/disabled/);
    expect(warn('allow', [], '', live).map((w) => w.text).join(' ')).toMatch(/allowlist is empty/);
    expect(warn('redirect', [], '', live).map((w) => w.text).join(' ')).toMatch(/needs a redirect address/);
  });

  it('warns loudly when real mail can go out', () => {
    const w = warn('allow', ['a@x.test'], '', live);
    expect(levels(w)).toContain('danger');
    expect(w.map((x) => x.text).join(' ')).toMatch(/Real email will be delivered/);
  });

  it('warns about whole domains and about the redirect target', () => {
    const w = warn('redirect', ['@corp.example', 'a@x.test'], 'pm@corp.example', live);
    const text = w.map((x) => x.text).join(' ');
    expect(text).toMatch(/Whole domains are allowed \(@corp.example\)/);
    expect(text).toMatch(/replaced by pm@corp.example/);
  });

  it('does not claim real delivery while the transport is off', () => {
    expect(levels(warn('allow', ['a@x.test'], '', { enabled: false }))).not.toContain('danger');
  });
});

describe('confirmationFor', () => {
  it('needs no confirmation for capture', () => {
    expect(confirmationFor('capture', ['a@x.test'], '')).toBe('');
  });
  it('lists the allowlist and the redirect target', () => {
    const text = confirmationFor('redirect', ['a@x.test'], ' pm@x.test ');
    expect(text).toContain('REAL email');
    expect(text).toContain('a@x.test');
    expect(text).toContain('redirected to pm@x.test');
    expect(confirmationFor('allow', [], '')).toContain('allowlist is empty');
  });
});

describe('transport form helpers', () => {
  it('validates hosts and ports', () => {
    expect(hostError('smtp.example.com')).toBe('');
    expect(hostError('127.0.0.1')).toBe('');
    expect(hostError('::1')).toBe('');
    expect(hostError('')).not.toBe('');
    expect(hostError('smtp://x.example')).not.toBe('');
    expect(hostError('x.example:25')).not.toBe('');
    expect(hostError('a..b')).not.toBe('');
    expect(portError('587')).toBe('');
    expect(portError('0')).not.toBe('');
    expect(portError('70000')).not.toBe('');
    expect(portError('abc')).not.toBe('');
  });

  it('builds the SES preset', () => {
    expect(sesPreset('eu-west-1')).toEqual({ host: 'email-smtp.eu-west-1.amazonaws.com', port: 587, tls_mode: 'starttls' });
  });

  it('detects a changed connection', () => {
    const stored = { kind: 'smtp' as const, host: 'h.example', port: 587, username: 'u', tls_mode: 'starttls' as const };
    const form = { kind: 'smtp', host: ' h.example ', port: 587, username: 'u', tls_mode: 'starttls' };
    expect(connectionChanged(stored, form)).toBe(false);
    expect(connectionChanged(stored, { ...form, host: 'other.example' })).toBe(true);
    expect(connectionChanged(stored, { ...form, port: 465 })).toBe(true);
    expect(connectionChanged(stored, { ...form, username: 'x' })).toBe(true);
    expect(connectionChanged(stored, { ...form, tls_mode: 'implicit' })).toBe(true);
    expect(connectionChanged(stored, { ...form, kind: 'ses' })).toBe(true);
  });

  it('names statuses', () => {
    expect(statusText('skipped')).toBe('Not sent');
    expect(statusText('weird')).toBe('weird');
  });
});

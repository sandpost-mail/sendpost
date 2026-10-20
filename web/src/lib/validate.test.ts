import { describe, expect, it } from 'vitest';
import * as v from './validate';

describe('validate', () => {
  it('email', () => {
    expect(v.email('a@example.test')).toBe('');
    expect(v.email('  a@example.test ')).toBe('');
    expect(v.email('')).not.toBe('');
    expect(v.email('no-at-sign')).not.toBe('');
    expect(v.email('a b@example.test')).not.toBe('');
  });

  it('newPassword enforces 12 to 128 characters', () => {
    expect(v.newPassword('x'.repeat(11))).not.toBe('');
    expect(v.newPassword('x'.repeat(12))).toBe('');
    expect(v.newPassword('x'.repeat(128))).toBe('');
    expect(v.newPassword('x'.repeat(129))).not.toBe('');
  });

  it('counts code points, not UTF-16 units', () => {
    expect(v.newPassword('😀'.repeat(12))).toBe('');
  });

  it('passwordConfirmation', () => {
    expect(v.passwordConfirmation('a', 'a')).toBe('');
    expect(v.passwordConfirmation('a', 'b')).not.toBe('');
  });

  it('group and project names', () => {
    expect(v.groupName('  ')).not.toBe('');
    expect(v.groupName('Devs')).toBe('');
    expect(v.groupName('x'.repeat(101))).not.toBe('');
    expect(v.projectName('x'.repeat(200))).toBe('');
  });

  it('id', () => {
    expect(v.id('42', 'Group id')).toBe('');
    for (const bad of ['', '0', '-1', '1.5', 'abc', '1e3']) expect(v.id(bad, 'Group id')).not.toBe('');
  });

  it('firstError', () => {
    expect(v.firstError('', 'b', 'c')).toBe('b');
    expect(v.firstError('', '')).toBe('');
  });
});

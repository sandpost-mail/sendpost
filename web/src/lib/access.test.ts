import { describe, expect, it } from 'vitest';
import { accessFor, isRole } from './access';
import type { Role } from './types';

describe('accessFor', () => {
  const cases: Array<[Role, { owner: boolean; canWrite: boolean }]> = [
    ['viewer', { owner: false, canWrite: false }],
    ['member', { owner: false, canWrite: true }],
    ['owner', { owner: true, canWrite: true }]
  ];

  it.each(cases)('maps %s', (role, want) => {
    expect(accessFor(role)).toEqual({ role, ...want });
  });

  it('never grants more than the role says', () => {
    expect(accessFor('viewer').owner).toBe(false);
    expect(accessFor('member').owner).toBe(false);
  });
});

describe('isRole', () => {
  it('accepts the three project roles only', () => {
    expect(['viewer', 'member', 'owner'].every(isRole)).toBe(true);
    expect(isRole('admin')).toBe(false);
    expect(isRole('')).toBe(false);
    expect(isRole(undefined)).toBe(false);
  });
});

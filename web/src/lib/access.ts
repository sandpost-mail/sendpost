import type { Role } from './types';

/** What the current user may do in one project, as far as the UI needs to know. */
export interface ProjectAccess {
  role: Role;
  /** Can manage credentials, tokens, tag rules, group roles and see the project audit. */
  owner: boolean;
  /** Can delete, tag and comment on messages (messages.write). Viewers can still read and mark read. */
  canWrite: boolean;
}

const ROLES: readonly Role[] = ['viewer', 'member', 'owner'];

/** Whether a value from the API is a project role this UI knows. */
export function isRole(value: unknown): value is Role {
  return typeof value === 'string' && (ROLES as readonly string[]).includes(value);
}

/**
 * Maps the effective role the API returns on a project to UI capabilities.
 * The server enforces every action either way, this only decides what to show.
 */
export function accessFor(role: Role): ProjectAccess {
  return { role, owner: role === 'owner', canWrite: role === 'member' || role === 'owner' };
}

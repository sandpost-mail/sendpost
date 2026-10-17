import { ApiError } from './api';
import type { Assignment, User } from './types';

/** What the current user may do in one project, as far as the UI needs to know. */
export interface ProjectAccess {
  /** Can manage credentials, tokens, tag rules, group roles and see the project audit. */
  owner: boolean;
}

interface GroupsApi {
  listProjectGroups: (projectId: number) => Promise<Assignment[]>;
}

/**
 * The API does not return the effective role, so it is derived. Instance
 * administrators are owners everywhere. For everyone else the owner only
 * endpoint `GET /projects/{id}/groups` answers 200 for owners and 403 for
 * viewers and members (who differ only in `messages.write`, which this UI does
 * not use yet). The server enforces every action either way, this only decides
 * what to show.
 */
export async function projectAccess(
  api: GroupsApi,
  user: Pick<User, 'is_admin'> | null,
  projectId: number
): Promise<ProjectAccess> {
  if (user?.is_admin) return { owner: true };
  try {
    await api.listProjectGroups(projectId);
    return { owner: true };
  } catch (e) {
    if (e instanceof ApiError && e.status === 403 && e.code !== 'password_change_required') {
      return { owner: false };
    }
    throw e;
  }
}

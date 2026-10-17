import { error } from '@sveltejs/kit';
import type { PageLoad } from './$types';

// The project audit log is for project owners (and administrators).
export const load: PageLoad = async ({ parent }) => {
  const { projectId, project, access } = await parent();
  if (!access.owner) error(403, 'Only project owners can see the audit log.');
  return { projectId, project };
};

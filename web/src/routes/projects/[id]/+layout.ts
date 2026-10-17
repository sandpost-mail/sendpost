import { error } from '@sveltejs/kit';
import { projectAccess } from '$lib/access';
import { client } from '$lib/api';
import { failLoad } from '$lib/loadError';
import type { LayoutLoad } from './$types';

export const load: LayoutLoad = async ({ params, fetch, parent }) => {
  const { user } = await parent();
  const projectId = Number(params.id);
  if (!Number.isInteger(projectId) || projectId <= 0) error(404, 'Project not found');
  const api = client(fetch);
  try {
    const [project, access] = await Promise.all([
      api.getProject(projectId),
      projectAccess(api, user, projectId)
    ]);
    return { projectId, project, access };
  } catch (e) {
    failLoad(e, 'project');
  }
};

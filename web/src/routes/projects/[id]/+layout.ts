import { error } from '@sveltejs/kit';
import { client } from '$lib/api';
import { loadProject } from '$lib/projectLoad';
import type { LayoutLoad } from './$types';

export const load: LayoutLoad = async ({ params, fetch, parent }) => {
  await parent();
  const projectId = Number(params.id);
  if (!Number.isInteger(projectId) || projectId <= 0) error(404, 'Project not found');
  return loadProject(client(fetch), projectId);
};

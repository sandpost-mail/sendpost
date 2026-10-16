import { error } from '@sveltejs/kit';
import { ApiError, client } from '$lib/api';
import type { LayoutLoad } from './$types';

export const load: LayoutLoad = async ({ params, fetch }) => {
  const projectId = Number(params.id);
  if (!Number.isInteger(projectId) || projectId <= 0) error(404, 'Project not found');
  try {
    const project = await client(fetch).getProject(projectId);
    return { projectId, project };
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) error(404, 'Project not found');
    error(502, e instanceof Error ? e.message : 'Could not load the project');
  }
};

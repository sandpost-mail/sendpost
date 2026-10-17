import { error } from '@sveltejs/kit';
import { client } from '$lib/api';
import { failLoad } from '$lib/loadError';
import type { PageLoad } from './$types';

export const load: PageLoad = async ({ parent, fetch }) => {
  const { projectId, project, access } = await parent();
  // Credentials, tokens, tag rules and access are for project owners (and administrators).
  if (!access.owner) error(403, 'Only project owners can change the settings of this project.');
  const api = client(fetch);
  try {
    const [credentials, rules, info] = await Promise.all([
      api.listCredentials(projectId),
      api.listTagRules(projectId),
      api.info().catch(() => null)
    ]);
    return { projectId, project, credentials, rules, info };
  } catch (e) {
    failLoad(e, 'project');
  }
};

import { client } from '$lib/api';
import type { PageLoad } from './$types';

export const load: PageLoad = async ({ parent, fetch }) => {
  const { projectId, project } = await parent();
  const api = client(fetch);
  const [credentials, rules, info] = await Promise.all([
    api.listCredentials(projectId),
    api.listTagRules(projectId),
    api.info().catch(() => null)
  ]);
  return { projectId, project, credentials, rules, info };
};

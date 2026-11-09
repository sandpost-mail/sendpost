// Everyone who can read the project can read its activity feed (it carries no
// owner only data), so there is no role check here: the server decides.
import type { PageLoad } from './$types';

export const load: PageLoad = async ({ parent }) => {
  const { projectId, project } = await parent();
  return { projectId, project };
};

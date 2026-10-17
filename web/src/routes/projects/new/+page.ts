import { error } from '@sveltejs/kit';
import type { PageLoad } from './$types';

// Creating projects is for instance administrators.
export const load: PageLoad = async ({ parent }) => {
  const { user } = await parent();
  if (!user?.is_admin) error(403, 'Only administrators can create projects.');
};

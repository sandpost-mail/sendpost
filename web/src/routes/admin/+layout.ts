import { error } from '@sveltejs/kit';
import type { LayoutLoad } from './$types';

// Everything under /admin is for instance administrators. The server enforces
// it as well, this only avoids showing pages that can not work.
export const load: LayoutLoad = async ({ parent }) => {
  const { user } = await parent();
  if (!user?.is_admin) error(403, 'Only administrators can open this page.');
};

import { error } from '@sveltejs/kit';
import { client } from '$lib/api';
import { failLoad } from '$lib/loadError';
import type { PageLoad } from './$types';

export const load: PageLoad = async ({ params, parent, fetch }) => {
  await parent();
  const gid = Number(params.gid);
  if (!Number.isInteger(gid) || gid <= 0) error(404, 'Group not found');
  const api = client(fetch);
  try {
    const [group, members, users] = await Promise.all([
      api.getGroup(gid),
      api.listGroupMembers(gid),
      api.listUsers()
    ]);
    return { group, members, users };
  } catch (e) {
    failLoad(e, 'group');
  }
};

import { error } from '@sveltejs/kit';
import { client } from '$lib/api';
import { failLoad } from '$lib/loadError';
import type { PageLoad } from './$types';

export const load: PageLoad = async ({ params, parent, fetch }) => {
  await parent();
  const uid = Number(params.uid);
  if (!Number.isInteger(uid) || uid <= 0) error(404, 'User not found');
  try {
    return { user: await client(fetch).getUser(uid) };
  } catch (e) {
    failLoad(e, 'user');
  }
};

import { client } from '$lib/api';
import { failLoad } from '$lib/loadError';
import type { PageLoad } from './$types';

export const load: PageLoad = async ({ parent, fetch }) => {
  await parent();
  try {
    return { users: await client(fetch).listUsers() };
  } catch (e) {
    failLoad(e, 'user list');
  }
};

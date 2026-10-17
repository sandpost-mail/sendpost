import { error } from '@sveltejs/kit';
import { client } from '$lib/api';
import { failLoad } from '$lib/loadError';
import type { PageLoad } from './$types';

export const load: PageLoad = async ({ params, parent, fetch }) => {
  const { projectId } = await parent();
  const messageId = Number(params.mid);
  if (!Number.isInteger(messageId) || messageId <= 0) error(404, 'Message not found');
  try {
    const message = await client(fetch).getMessage(projectId, messageId);
    return { projectId, message };
  } catch (e) {
    failLoad(e, 'message');
  }
};

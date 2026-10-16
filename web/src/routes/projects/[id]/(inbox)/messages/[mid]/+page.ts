import { error } from '@sveltejs/kit';
import { ApiError, client } from '$lib/api';
import type { PageLoad } from './$types';

export const load: PageLoad = async ({ params, parent, fetch }) => {
  const { projectId } = await parent();
  const messageId = Number(params.mid);
  if (!Number.isInteger(messageId) || messageId <= 0) error(404, 'Message not found');
  try {
    const message = await client(fetch).getMessage(projectId, messageId);
    return { projectId, message };
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) error(404, 'Message not found');
    error(502, e instanceof Error ? e.message : 'Could not load the message');
  }
};

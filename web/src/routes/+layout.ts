import { error, redirect } from '@sveltejs/kit';
import { guardRedirect } from '$lib/guard';
import { session } from '$lib/session.svelte';
import type { LayoutLoad } from './$types';

// Single page application: no server side rendering.
export const ssr = false;
export const prerender = false;

// Every navigation passes here first: find out who is signed in, then send
// visitors to the login page or the forced password change when needed.
// Child loads call `await parent()` so they never run unauthenticated.
export const load: LayoutLoad = async ({ fetch, url }) => {
  try {
    await session.ensure(fetch);
  } catch (e) {
    error(502, e instanceof Error ? e.message : 'Could not reach the server');
  }
  const target = guardRedirect(session.user, url.pathname, url.search);
  if (target) redirect(307, target);
  return { user: session.user };
};

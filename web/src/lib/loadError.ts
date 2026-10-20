import { error } from '@sveltejs/kit';
import { ApiError } from './api';

/**
 * Turn a failed API call in a load function into a SvelteKit error page.
 * `noun` is what was being loaded ("project", "message"). 401 and
 * `password_change_required` are redirected by the root layout (see
 * `guardRedirect`), so the page shown for them is only a short lived placeholder.
 */
export function failLoad(e: unknown, noun: string): never {
  if (e instanceof ApiError) {
    if (e.status === 401) error(401, 'Your session has ended. Sign in again.');
    if (e.status === 403 && e.code === 'password_change_required') {
      error(403, 'Change your password to continue.');
    }
    if (e.status === 403) error(403, `You do not have access to this ${noun}.`);
    if (e.status === 404) error(404, `${noun.charAt(0).toUpperCase()}${noun.slice(1)} not found`);
  }
  error(502, e instanceof Error ? e.message : `Could not load the ${noun}`);
}

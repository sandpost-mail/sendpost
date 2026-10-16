import { projectUrl } from './api';
import type { LiveEventType } from './types';

export type LiveStatus = 'connecting' | 'live' | 'offline';

export interface LiveHandlers {
  /** Called for every event after `ready`, and once on each (re)connect. */
  onEvent: (type: LiveEventType) => void;
  onStatus: (status: LiveStatus) => void;
}

const EVENT_TYPES: LiveEventType[] = ['ready', 'message.created', 'resync'];

/**
 * Subscribe to a project's server-sent events. EventSource reconnects by
 * itself, and the server sends `ready` after every reconnect, which is the
 * signal to reload so nothing published while offline is missed.
 * Returns a function that closes the stream.
 */
export function subscribeToProject(projectId: number, handlers: LiveHandlers): () => void {
  handlers.onStatus('connecting');
  const source = new EventSource(`${projectUrl(projectId)}/events`);
  source.onerror = () => handlers.onStatus('offline');
  for (const type of EVENT_TYPES) {
    source.addEventListener(type, () => {
      if (type === 'ready') handlers.onStatus('live');
      handlers.onEvent(type);
    });
  }
  return () => source.close();
}

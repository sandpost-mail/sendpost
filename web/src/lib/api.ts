import type {
  Credential,
  IssuedCredential,
  MessageDetail,
  MessagePage,
  Project,
  ServerInfo,
  TagRule,
  TagRuleInput
} from './types';

const BASE = '/_/api/v1';

/** An API call that failed. `message` is safe to show to the user. */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string
  ) {
    super(message);
    this.name = 'ApiError';
  }
}

type Fetch = typeof fetch;

async function request<T>(
  f: Fetch,
  method: string,
  path: string,
  body?: unknown
): Promise<T> {
  const init: RequestInit = { method };
  if (method !== 'GET') {
    // The API only accepts JSON writes, which blocks cross site form posts.
    init.headers = { 'Content-Type': 'application/json' };
    init.body = JSON.stringify(body ?? {});
  }
  const res = await f(BASE + path, init);
  if (!res.ok) {
    let message = `Request failed (${res.status})`;
    try {
      const data = await res.json();
      if (typeof data?.error === 'string') message = data.error;
    } catch {
      // keep the generic message
    }
    throw new ApiError(res.status, message);
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

export const projectUrl = (projectId: number) => `${BASE}/projects/${projectId}`;
export const messageUrl = (projectId: number, id: number) =>
  `${projectUrl(projectId)}/messages/${id}`;

/** Build the typed API client around a fetch implementation (SvelteKit's `fetch` in load functions). */
export function client(f: Fetch = fetch) {
  const get = <T>(path: string) => request<T>(f, 'GET', path);
  const send = <T>(method: string, path: string, body?: unknown) =>
    request<T>(f, method, path, body);

  return {
    info: () => get<ServerInfo>('/info'),
    listProjects: async () => (await get<{ items: Project[] }>('/projects')).items,
    getProject: (id: number) => get<Project>(`/projects/${id}`),
    createProject: (name: string, slug = '') => send<Project>('POST', '/projects', { name, slug }),

    listMessages: (projectId: number, q: string, cursor = '', limit = 50) => {
      const params = new URLSearchParams();
      if (q.trim()) params.set('q', q.trim());
      if (cursor) params.set('cursor', cursor);
      params.set('limit', String(limit));
      return get<MessagePage>(`/projects/${projectId}/messages?${params}`);
    },
    getMessage: (projectId: number, id: number) =>
      get<MessageDetail>(`/projects/${projectId}/messages/${id}`),
    markRead: (projectId: number, id: number) =>
      send<void>('POST', `/projects/${projectId}/messages/${id}/read`),
    rawMessage: async (projectId: number, id: number) => {
      const res = await f(`${BASE}/projects/${projectId}/messages/${id}/raw`);
      if (!res.ok) throw new ApiError(res.status, `Request failed (${res.status})`);
      return res.text();
    },

    listCredentials: async (projectId: number) =>
      (await get<{ items: Credential[] }>(`/projects/${projectId}/credentials`)).items,
    createCredential: (projectId: number, label: string) =>
      send<IssuedCredential>('POST', `/projects/${projectId}/credentials`, { label }),
    revokeCredential: (projectId: number, id: number) =>
      send<void>('DELETE', `/projects/${projectId}/credentials/${id}`),

    listTagRules: async (projectId: number) =>
      (await get<{ items: TagRule[] }>(`/projects/${projectId}/tag-rules`)).items,
    createTagRule: (projectId: number, rule: TagRuleInput) =>
      send<TagRule>('POST', `/projects/${projectId}/tag-rules`, rule),
    deleteTagRule: (projectId: number, id: number) =>
      send<void>('DELETE', `/projects/${projectId}/tag-rules/${id}`)
  };
}

export const api = client();

/** A message from an unknown error, for display. */
export function errorMessage(e: unknown): string {
  return e instanceof Error ? e.message : 'Something went wrong';
}

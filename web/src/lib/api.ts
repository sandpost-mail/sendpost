import type {
  Assignment,
  AuditPage,
  Credential,
  Group,
  IssuedCredential,
  IssuedToken,
  MeInfo,
  MessageDetail,
  MessagePage,
  PreviewUrl,
  Project,
  Role,
  ServerInfo,
  SessionInfo,
  TagRule,
  TagRuleInput,
  Token,
  TokenInput,
  User,
  UserInput,
  UserPatch
} from './types';

const BASE = '/_/api/v1';

/** An API call that failed. `message` is safe to show to the user. */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
    /** Stable machine readable code from the server, empty when there is none. */
    readonly code = ''
  ) {
    super(message);
    this.name = 'ApiError';
  }
}

/** Reactions to the failures every screen has to handle the same way. */
export interface AuthHooks {
  /** 401: the session is missing or expired. */
  unauthenticated: () => void;
  /** 403 `password_change_required`: only the password form is allowed. */
  passwordChangeRequired: () => void;
  /** 403 `csrf_invalid`: the token is stale, fetch a fresh one. */
  csrfInvalid: () => void;
}

let csrfToken = '';
let hooks: Partial<AuthHooks> = {};

/** The token sent as X-CSRF-Token on every unsafe request (kept in memory only). */
export function setCsrfToken(token: string): void {
  csrfToken = token;
}

export function setAuthHooks(next: Partial<AuthHooks>): void {
  hooks = next;
}

type Fetch = typeof fetch;

/** Paths where a 401 means "wrong credentials" or is handled by the caller. */
const OWN_401 = new Set(['/auth/login', '/auth/me']);

function notifyAuthFailure(status: number, code: string, path: string): void {
  if (status === 401 && !OWN_401.has(path)) hooks.unauthenticated?.();
  else if (status === 403 && code === 'password_change_required') hooks.passwordChangeRequired?.();
  else if (status === 403 && code === 'csrf_invalid') hooks.csrfInvalid?.();
}

async function request<T>(f: Fetch, method: string, path: string, body?: unknown): Promise<T> {
  const init: RequestInit = { method, credentials: 'same-origin' };
  if (method !== 'GET') {
    // The API only accepts JSON writes, which blocks cross site form posts.
    const headers: Record<string, string> = { 'Content-Type': 'application/json' };
    if (csrfToken) headers['X-CSRF-Token'] = csrfToken;
    init.headers = headers;
    init.body = JSON.stringify(body ?? {});
  }
  const res = await f(BASE + path, init);
  if (!res.ok) {
    let message = `Request failed (${res.status})`;
    let code = '';
    try {
      const data = await res.json();
      if (typeof data?.error === 'string') message = data.error;
      if (typeof data?.code === 'string') code = data.code;
    } catch {
      // keep the generic message
    }
    notifyAuthFailure(res.status, code, path);
    throw new ApiError(res.status, message, code);
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

export const projectUrl = (projectId: number) => `${BASE}/projects/${projectId}`;
export const messageUrl = (projectId: number, id: number) =>
  `${projectUrl(projectId)}/messages/${id}`;

function pageQuery(cursor: string, limit: number): string {
  const params = new URLSearchParams();
  if (cursor) params.set('cursor', cursor);
  params.set('limit', String(limit));
  return params.toString();
}

/** Build the typed API client around a fetch implementation (SvelteKit's `fetch` in load functions). */
export function client(f: Fetch = fetch) {
  const get = <T>(path: string) => request<T>(f, 'GET', path);
  const send = <T>(method: string, path: string, body?: unknown) =>
    request<T>(f, method, path, body);
  const items = async <T>(path: string) => (await get<{ items: T[] }>(path)).items;

  return {
    info: () => get<ServerInfo>('/info'),

    // Session.
    me: () => get<MeInfo>('/auth/me'),
    login: (email: string, password: string) =>
      send<SessionInfo>('POST', '/auth/login', { email, password }),
    logout: () => send<void>('POST', '/auth/logout'),
    changePassword: (current_password: string, new_password: string) =>
      send<void>('POST', '/auth/password', { current_password, new_password }),

    // Own tokens.
    listMyTokens: () => items<Token>('/users/me/tokens'),
    createMyToken: (input: TokenInput) => send<IssuedToken>('POST', '/users/me/tokens', input),
    revokeMyToken: (id: number) => send<void>('DELETE', `/users/me/tokens/${id}`),

    // Users (administrators).
    listUsers: () => items<User>('/users'),
    getUser: (id: number) => get<User>(`/users/${id}`),
    createUser: (input: UserInput) => send<User>('POST', '/users', input),
    updateUser: (id: number, patch: UserPatch) => send<User>('PATCH', `/users/${id}`, patch),
    resetPassword: (id: number, new_password: string) =>
      send<void>('POST', `/users/${id}/password`, { new_password }),
    listUserTokens: (id: number) => items<Token>(`/users/${id}/tokens`),
    revokeUserToken: (uid: number, tid: number) =>
      send<void>('DELETE', `/users/${uid}/tokens/${tid}`),

    // Groups (administrators).
    listGroups: () => items<Group>('/groups'),
    getGroup: (id: number) => get<Group>(`/groups/${id}`),
    createGroup: (name: string) => send<Group>('POST', '/groups', { name }),
    renameGroup: (id: number, name: string) => send<Group>('PATCH', `/groups/${id}`, { name }),
    deleteGroup: (id: number) => send<void>('DELETE', `/groups/${id}`),
    listGroupMembers: (id: number) => items<User>(`/groups/${id}/members`),
    addGroupMember: (gid: number, uid: number) =>
      send<void>('PUT', `/groups/${gid}/members/${uid}`),
    removeGroupMember: (gid: number, uid: number) =>
      send<void>('DELETE', `/groups/${gid}/members/${uid}`),

    listAudit: (cursor = '', limit = 50) => get<AuditPage>(`/audit?${pageQuery(cursor, limit)}`),

    // Projects.
    listProjects: () => items<Project>('/projects'),
    getProject: (id: number) => get<Project>(`/projects/${id}`),
    createProject: (name: string, slug = '') => send<Project>('POST', '/projects', { name, slug }),
    listProjectGroups: (projectId: number) => items<Assignment>(`/projects/${projectId}/groups`),
    assignProjectGroup: (projectId: number, groupId: number, role: Role) =>
      send<void>('PUT', `/projects/${projectId}/groups/${groupId}`, { role }),
    removeProjectGroup: (projectId: number, groupId: number) =>
      send<void>('DELETE', `/projects/${projectId}/groups/${groupId}`),
    listProjectAudit: (projectId: number, cursor = '', limit = 50) =>
      get<AuditPage>(`/projects/${projectId}/audit?${pageQuery(cursor, limit)}`),
    listProjectTokens: (projectId: number) => items<Token>(`/projects/${projectId}/tokens`),
    createProjectToken: (projectId: number, input: TokenInput) =>
      send<IssuedToken>('POST', `/projects/${projectId}/tokens`, input),
    revokeProjectToken: (projectId: number, tid: number) =>
      send<void>('DELETE', `/projects/${projectId}/tokens/${tid}`),

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
    /** A short lived signed URL for the sandboxed HTML preview iframe (it carries no cookie). */
    previewUrl: (projectId: number, id: number) =>
      get<PreviewUrl>(`/projects/${projectId}/messages/${id}/preview-url`),
    rawMessage: async (projectId: number, id: number) => {
      const res = await f(`${BASE}/projects/${projectId}/messages/${id}/raw`, {
        credentials: 'same-origin'
      });
      if (!res.ok) {
        notifyAuthFailure(res.status, '', '/raw');
        throw new ApiError(res.status, `Request failed (${res.status})`);
      }
      return res.text();
    },

    listCredentials: (projectId: number) => items<Credential>(`/projects/${projectId}/credentials`),
    createCredential: (projectId: number, label: string) =>
      send<IssuedCredential>('POST', `/projects/${projectId}/credentials`, { label }),
    revokeCredential: (projectId: number, id: number) =>
      send<void>('DELETE', `/projects/${projectId}/credentials/${id}`),

    listTagRules: (projectId: number) => items<TagRule>(`/projects/${projectId}/tag-rules`),
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

// Shapes returned by the Sandpost native API (/_/api/v1).

export type Role = 'viewer' | 'member' | 'owner';

export interface Project {
  id: number;
  slug: string;
  name: string;
  created_at: string;
  /** The caller's effective role on the project. Instance administrators are owners. */
  role: Role;
}

export interface ServerInfo {
  smtp_port: number;
  smtp_tls_mode: 'off' | 'starttls' | 'required' | 'implicit';
  version: string;
}

export interface MessageSummary {
  id: number;
  received_at: string;
  from: string;
  to: string[];
  subject: string;
  size_bytes: number;
  read_at: string | null;
  tags: string[];
  attachment_count: number;
}

export interface MessagePage {
  items: MessageSummary[];
  next_cursor: string;
}

export interface Recipient {
  kind: 'to' | 'cc' | 'bcc' | 'envelope';
  address: string;
}

export interface Attachment {
  ordinal: number;
  filename: string;
  content_type: string;
  size_bytes: number;
  content_id: string;
  inline: boolean;
}

export interface MessageDetail {
  id: number;
  received_at: string;
  message_id: string;
  from: string;
  subject: string;
  size_bytes: number;
  text: string;
  has_html: boolean;
  headers: Record<string, string[]>;
  recipients: Recipient[];
  attachments: Attachment[];
  links: string[];
  tags: string[];
  read_at: string | null;
}

export interface Credential {
  id: number;
  project_id: number;
  username: string;
  label: string;
  created_at: string;
  revoked_at: string | null;
  last_used_at: string | null;
}

export interface IssuedCredential extends Credential {
  secret: string;
}

export interface TagRule {
  id: number;
  project_id: number;
  kind: 'header' | 'recipient';
  header_name: string;
  pattern: string;
  tag: string;
  created_at: string;
}

export interface TagRuleInput {
  kind: 'header' | 'recipient';
  header_name: string;
  pattern: string;
  tag: string;
}

export type LiveEventType = 'ready' | 'message.created' | 'resync';

export interface User {
  id: number;
  email: string;
  name: string;
  is_admin: boolean;
  must_change_password: boolean;
  created_at: string;
  disabled_at: string | null;
}

export interface UserInput {
  email: string;
  name: string;
  password: string;
  is_admin: boolean;
}

export interface UserPatch {
  name?: string;
  is_admin?: boolean;
  disabled?: boolean;
}

/** Answer of POST /auth/login. */
export interface SessionInfo {
  user: User;
  csrf_token: string;
  expires_at: string;
}

/** Answer of GET /auth/me. The CSRF token is only present for browser sessions. */
export interface MeInfo {
  user: User;
  auth_kind: 'session' | 'user_token';
  csrf_token?: string;
  expires_at?: string;
}

export interface Group {
  id: number;
  name: string;
  created_at: string;
  /** Only filled by the list endpoint. */
  member_count: number;
}

export interface Assignment {
  group_id: number;
  group_name: string;
  role: Role;
}

export type TokenScope = 'read' | 'write' | 'manage';

export interface TokenInput {
  label: string;
  scopes: TokenScope[];
}

export interface Token {
  id: number;
  owner_user_id: number | null;
  project_id: number | null;
  prefix: string;
  label: string;
  scopes: TokenScope[];
  created_at: string;
  revoked_at: string | null;
  last_used_at: string | null;
}

export interface IssuedToken extends Token {
  secret: string;
}

export interface AuditEvent {
  id: number;
  at: string;
  actor_user_id: number | null;
  actor_email: string;
  project_id: number | null;
  action: string;
  target: string;
  detail: Record<string, unknown>;
}

export interface AuditPage {
  items: AuditEvent[];
  next_cursor: string;
}

export interface PreviewUrl {
  url: string;
  expires_at: string;
}

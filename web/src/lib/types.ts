// Shapes returned by the Sandpost native API (/_/api/v1).

export interface Project {
  id: number;
  slug: string;
  name: string;
  created_at: string;
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

import type { ServerInfo } from './types';

export interface SnippetInput {
  host: string;
  info: ServerInfo | null;
  username: string;
  /** The secret, only known right after creation. */
  secret?: string;
}

export const SECRET_PLACEHOLDER = '<secret shown once at creation>';

/** A .env style block an app can use to send mail to this Sandpost. */
export function envSnippet({ host, info, username, secret }: SnippetInput): string {
  const port = info?.smtp_port ?? 1025;
  const password = secret ?? SECRET_PLACEHOLDER;
  const lines = [
    `SMTP_HOST=${host}`,
    `SMTP_PORT=${port}`,
    `SMTP_USERNAME=${username}`,
    `SMTP_PASSWORD=${password}`
  ];
  if (info?.smtp_tls_mode === 'implicit') lines.push('SMTP_SECURE=true # implicit TLS');
  else if (info?.smtp_tls_mode === 'required') lines.push('SMTP_STARTTLS=required');
  else if (info?.smtp_tls_mode === 'starttls') lines.push('SMTP_STARTTLS=optional');
  if (secret) lines.push(`SMTP_URL=smtp://${username}:${secret}@${host}:${port}`);
  return lines.join('\n');
}

// Pure helpers for the delivery settings screens. The server (internal/policy)
// decides what is actually sent. These only explain the consequences of a
// setting before it is saved, so nobody enables real delivery by accident.

import type { DeliveryMode, DeliveryPolicy, Transport } from './types';

/** Splits what a person typed (one entry per line, commas also work) into entries. */
export function parseAllowlist(text: string): string[] {
  return text
    .split(/[\n,;]+/)
    .map((s) => s.trim())
    .filter((s) => s !== '');
}

export function formatAllowlist(entries: string[]): string {
  return entries.join('\n');
}

/** A whole domain entry ("@example.com" or "example.com"), as opposed to one address. */
export function isDomainEntry(entry: string): boolean {
  const e = entry.trim();
  return !e.includes('@') || e.startsWith('@');
}

export interface Warning {
  /** danger: real mail may go out. info: nothing will be sent. */
  level: 'danger' | 'info';
  text: string;
}

/**
 * What the saved settings mean in practice. `policy` and `transport` are the
 * stored state (or the form state the user is about to save).
 */
export function deliveryWarnings(
  policy: Pick<DeliveryPolicy, 'mode' | 'allowlist' | 'redirect_address'>,
  transport: Pick<Transport, 'enabled'> | null
): Warning[] {
  if (policy.mode === 'capture') {
    return [{ level: 'info', text: 'Capture only: mail stays in Sandpost and nothing is ever sent out.' }];
  }
  const out: Warning[] = [];
  if (!transport) {
    out.push({ level: 'info', text: 'No transport is configured, so nothing will be sent. Set one up first.' });
  } else if (!transport.enabled) {
    out.push({ level: 'info', text: 'The transport is disabled, so nothing will be sent until you enable it.' });
  }
  if (policy.mode === 'redirect' && !policy.redirect_address.trim()) {
    out.push({ level: 'info', text: 'Redirect mode needs a redirect address, otherwise nothing is sent.' });
  }
  if (policy.mode === 'allow' && policy.allowlist.length === 0) {
    out.push({ level: 'info', text: 'The allowlist is empty, so no recipient will receive mail.' });
  }
  const live = transport?.enabled === true;
  if (live && policy.allowlist.length > 0) {
    const domains = policy.allowlist.filter(isDomainEntry);
    out.push({
      level: 'danger',
      text:
        'Real email will be delivered to the people on the allowlist, not only captured. ' +
        'Check every entry: these recipients receive the messages your app sends.'
    });
    if (domains.length > 0) {
      out.push({
        level: 'danger',
        text: `Whole domains are allowed (${domains.join(', ')}): everyone at these domains can receive real mail.`
      });
    }
  }
  if (live && policy.mode === 'redirect' && policy.redirect_address.trim()) {
    out.push({
      level: 'danger',
      text: `Every other recipient is replaced by ${policy.redirect_address.trim()}, which receives a copy of all that mail.`
    });
  }
  return out;
}

/** What to ask before saving a policy that can send real mail, or '' when no confirmation is needed. */
export function confirmationFor(mode: DeliveryMode, allowlist: string[], redirect: string): string {
  if (mode === 'capture') return '';
  const lines = [
    mode === 'allow'
      ? 'Allow mode: recipients on the allowlist receive REAL email, the others get nothing.'
      : `Redirect mode: allowlisted recipients receive REAL email, everyone else is redirected to ${redirect.trim()}.`,
    '',
    allowlist.length > 0 ? `Allowlist:\n${allowlist.join('\n')}` : 'The allowlist is empty.',
    '',
    'Mail is only sent while the transport is enabled. Save this policy?'
  ];
  return lines.join('\n');
}

const HOST = /^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$/;

/** A host name or IP address without scheme, port or path. */
export function hostError(host: string): string {
  const h = host.trim();
  if (!h) return 'Host is required.';
  if (h.includes(':') && /^[0-9a-fA-F:]+$/.test(h)) return ''; // IPv6
  if (!HOST.test(h) || h.includes('..')) return 'Enter a host name or IP address, without scheme or port.';
  return '';
}

export function portError(port: string): string {
  const n = Number(port);
  return Number.isInteger(n) && n >= 1 && n <= 65535 ? '' : 'Port must be between 1 and 65535.';
}

/** Typical settings of Amazon SES SMTP in a region. The user still enters the SMTP credentials. */
export function sesPreset(region: string) {
  return { host: `email-smtp.${region}.amazonaws.com`, port: 587, tls_mode: 'starttls' as const };
}

const STATUS_TEXT: Record<string, string> = {
  pending: 'Pending',
  sent: 'Sent',
  failed: 'Failed',
  skipped: 'Not sent',
  blocked: 'Blocked'
};

export function statusText(status: string): string {
  return STATUS_TEXT[status] ?? status;
}

/** Whether the connection fields differ, which disables the transport and needs the password again. */
export function connectionChanged(
  stored: Pick<Transport, 'kind' | 'host' | 'port' | 'username' | 'tls_mode'>,
  form: { kind: string; host: string; port: number; username: string; tls_mode: string }
): boolean {
  return (
    stored.kind !== form.kind ||
    stored.host !== form.host.trim() ||
    stored.port !== form.port ||
    stored.username !== form.username.trim() ||
    stored.tls_mode !== form.tls_mode
  );
}

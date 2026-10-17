// Client side checks that mirror the API limits (docs/openapi.yaml). The server
// stays the authority, these only give fast, specific feedback. Each returns an
// error message, or an empty string when the value is fine.

export const PASSWORD_MIN = 12;
export const PASSWORD_MAX = 128;

const EMAIL = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

export function email(value: string): string {
  const v = value.trim();
  if (!v) return 'Enter an email address.';
  if (v.length > 254 || !EMAIL.test(v)) return 'Enter a valid email address.';
  return '';
}

export function newPassword(value: string): string {
  // Count code points so the limit matches what a person sees.
  const length = [...value].length;
  if (length < PASSWORD_MIN) return `Use at least ${PASSWORD_MIN} characters.`;
  if (length > PASSWORD_MAX) return `Use at most ${PASSWORD_MAX} characters.`;
  return '';
}

export function passwordConfirmation(password: string, confirmation: string): string {
  return password === confirmation ? '' : 'The two passwords do not match.';
}

export function required(value: string, label: string, max: number): string {
  const v = value.trim();
  if (!v) return `${label} is required.`;
  if ([...v].length > max) return `${label} must be at most ${max} characters.`;
  return '';
}

export const groupName = (value: string) => required(value, 'Group name', 100);
export const projectName = (value: string) => required(value, 'Project name', 200);

export function optionalMax(value: string, label: string, max: number): string {
  return [...value.trim()].length > max ? `${label} must be at most ${max} characters.` : '';
}

/** A positive integer id typed by a person. */
export function id(value: string, label: string): string {
  return /^[1-9]\d{0,17}$/.test(value.trim()) ? '' : `${label} must be a positive number.`;
}

/** The first non empty message of a list of checks, for forms with several fields. */
export function firstError(...messages: string[]): string {
  return messages.find((m) => m !== '') ?? '';
}

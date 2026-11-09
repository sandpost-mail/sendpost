// Client side checks for comments. The server stays the authority
// (docs/openapi.yaml): 1 to 2000 characters, line breaks and tabs allowed.

export const COMMENT_MAX = 2000;

/** Characters as a person counts them (code points), like the server. */
export function commentLength(text: string): number {
  return [...text.trim()].length;
}

/** An error message, or an empty string when the text can be sent. */
export function validateComment(text: string): string {
  const n = commentLength(text);
  if (n === 0) return 'Write a comment first.';
  if (n > COMMENT_MAX) return `Use at most ${COMMENT_MAX} characters (${n} now).`;
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f-\u009f]/.test(text)) return 'Remove the control characters.';
  return '';
}

/** Absolute permalink of a message. It needs a sign in and access to the project. */
export function messagePermalink(origin: string, projectId: number, messageId: number): string {
  return `${origin}/projects/${projectId}/messages/${messageId}`;
}

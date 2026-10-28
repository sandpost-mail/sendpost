import { describe, expect, it } from 'vitest';
import { COMMENT_MAX, commentLength, messagePermalink, validateComment } from './comments';

describe('validateComment', () => {
  it('accepts text, markup and line breaks', () => {
    expect(validateComment('hello\nworld\t<b>&</b>')).toBe('');
  });
  it('rejects empty and blank text', () => {
    expect(validateComment('')).not.toBe('');
    expect(validateComment(' \n ')).not.toBe('');
  });
  it('counts characters, not bytes', () => {
    expect(validateComment('é'.repeat(COMMENT_MAX))).toBe('');
    expect(validateComment('😀'.repeat(COMMENT_MAX))).toBe('');
    expect(validateComment('a'.repeat(COMMENT_MAX + 1))).toContain('at most 2000');
    expect(commentLength('😀')).toBe(1);
  });
  it('rejects control characters', () => {
    expect(validateComment('a\u0000b')).toContain('control');
    expect(validateComment('a\u001bb')).toContain('control');
  });
});

describe('messagePermalink', () => {
  it('is absolute and project scoped', () => {
    expect(messagePermalink('https://mail.example', 3, 42)).toBe('https://mail.example/projects/3/messages/42');
  });
});

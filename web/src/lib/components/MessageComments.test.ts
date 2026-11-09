import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Comment } from '$lib/types';

const api = vi.hoisted(() => ({ listComments: vi.fn(), createComment: vi.fn() }));
vi.mock('$lib/api', async (orig) => ({ ...(await orig<typeof import('$lib/api')>()), api }));

import MessageComments from './MessageComments.svelte';

const comment = (over: Partial<Comment> = {}): Comment => ({
  id: 1,
  message_id: 5,
  user_id: 2,
  author: 'alice@example.test',
  body: 'looks right',
  created_at: '2026-01-01T00:00:00Z',
  ...over
});

afterEach(() => vi.resetAllMocks());

describe('MessageComments', () => {
  it('explains an empty thread', async () => {
    api.listComments.mockResolvedValue({ items: [], next_cursor: '' });
    render(MessageComments, { projectId: 1, messageId: 5, canComment: true });
    expect(await screen.findByText('No comments yet.')).toBeInTheDocument();
  });

  it('renders the text as text, never as markup', async () => {
    const xss = '<img src=x onerror=alert(1)><script>alert(2)</script>';
    api.listComments.mockResolvedValue({ items: [comment({ body: xss, author: '<b>bob</b>' })], next_cursor: '' });
    const { container } = render(MessageComments, { projectId: 1, messageId: 5, canComment: false });
    expect(await screen.findByText(xss)).toBeInTheDocument();
    expect(screen.getByText('<b>bob</b>')).toBeInTheDocument();
    expect(container.querySelector('img, script, b')).toBeNull();
  });

  it('follows every page of the thread', async () => {
    api.listComments
      .mockResolvedValueOnce({ items: [comment({ id: 1, body: 'one' })], next_cursor: '1' })
      .mockResolvedValueOnce({ items: [comment({ id: 2, body: 'two' })], next_cursor: '' });
    render(MessageComments, { projectId: 1, messageId: 5, canComment: false });
    expect(await screen.findByText('two')).toBeInTheDocument();
    expect(screen.getByText('one')).toBeInTheDocument();
    expect(api.listComments).toHaveBeenLastCalledWith(1, 5, '1');
  });

  it('hides the form from viewers', async () => {
    api.listComments.mockResolvedValue({ items: [], next_cursor: '' });
    render(MessageComments, { projectId: 1, messageId: 5, canComment: false });
    await screen.findByText('No comments yet.');
    expect(screen.queryByLabelText('Add a comment')).not.toBeInTheDocument();
    expect(screen.getByText(/Viewers can read comments/)).toBeInTheDocument();
  });

  it('posts a comment and shows it', async () => {
    api.listComments.mockResolvedValue({ items: [], next_cursor: '' });
    api.createComment.mockResolvedValue(comment({ id: 9, body: 'new one' }));
    render(MessageComments, { projectId: 1, messageId: 5, canComment: true });
    await screen.findByText('No comments yet.');
    await fireEvent.input(screen.getByRole('textbox'), { target: { value: 'new one' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Comment' }));
    expect(await screen.findByText('new one')).toBeInTheDocument();
    expect(api.createComment).toHaveBeenCalledWith(1, 5, 'new one');
    expect((screen.getByRole('textbox') as HTMLTextAreaElement).value).toBe('');
  });

  it('refuses too long text without calling the API', async () => {
    api.listComments.mockResolvedValue({ items: [], next_cursor: '' });
    render(MessageComments, { projectId: 1, messageId: 5, canComment: true });
    await screen.findByText('No comments yet.');
    await fireEvent.input(screen.getByRole('textbox'), { target: { value: 'a'.repeat(2001) } });
    await fireEvent.click(screen.getByRole('button', { name: 'Comment' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('at most 2000');
    expect(api.createComment).not.toHaveBeenCalled();
  });

  it('shows the server error and keeps the text', async () => {
    api.listComments.mockResolvedValue({ items: [], next_cursor: '' });
    api.createComment.mockRejectedValue(new Error('forbidden'));
    render(MessageComments, { projectId: 1, messageId: 5, canComment: true });
    await screen.findByText('No comments yet.');
    await fireEvent.input(screen.getByRole('textbox'), { target: { value: 'keep me' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Comment' }));
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('forbidden'));
    expect((screen.getByRole('textbox') as HTMLTextAreaElement).value).toBe('keep me');
  });

  it('reloads when a live event arrives', async () => {
    api.listComments.mockResolvedValueOnce({ items: [], next_cursor: '' });
    const { rerender } = render(MessageComments, { projectId: 1, messageId: 5, canComment: false, liveVersion: 0 });
    await screen.findByText('No comments yet.');
    api.listComments.mockResolvedValueOnce({ items: [comment({ body: 'from someone else' })], next_cursor: '' });
    await rerender({ projectId: 1, messageId: 5, canComment: false, liveVersion: 1 });
    expect(await screen.findByText('from someone else')).toBeInTheDocument();
  });
});

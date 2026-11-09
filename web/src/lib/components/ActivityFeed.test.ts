import { fireEvent, render, screen } from '@testing-library/svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { ActivityItem } from '$lib/types';

const api = vi.hoisted(() => ({ listActivity: vi.fn() }));
vi.mock('$lib/api', async (orig) => ({ ...(await orig<typeof import('$lib/api')>()), api }));

import ActivityFeed from './ActivityFeed.svelte';

const item = (over: Partial<ActivityItem>): ActivityItem => ({
  at: '2026-01-01T00:00:00Z',
  kind: 'message',
  id: 1,
  message_id: 5,
  subject: 'Welcome',
  actor: 'noreply@app.test',
  detail: '',
  ...over
});

afterEach(() => vi.resetAllMocks());

describe('ActivityFeed', () => {
  it('explains an empty feed', async () => {
    api.listActivity.mockResolvedValue({ items: [], next_cursor: '' });
    render(ActivityFeed, { projectId: 1 });
    expect(await screen.findByText(/Nothing has happened/)).toBeInTheDocument();
  });

  it('links every entry to the message permalink and shows its kind', async () => {
    api.listActivity.mockResolvedValue({
      items: [
        item({ kind: 'comment', id: 3, actor: 'alice@example.test', detail: '<b>check</b>' }),
        item({ kind: 'release', id: 2, actor: 'owner@example.test', detail: 'qa@corp.example' }),
        item({ kind: 'message', id: 5, message_id: 5 }),
        item({ kind: 'release', id: 4, message_id: 0, subject: '' })
      ],
      next_cursor: ''
    });
    const { container } = render(ActivityFeed, { projectId: 7 });
    expect(await screen.findByText('commented')).toBeInTheDocument();
    expect(screen.getAllByRole('link', { name: 'Welcome' })[0]).toHaveAttribute('href', '/projects/7/messages/5');
    expect(screen.getByText('<b>check</b>')).toBeInTheDocument();
    expect(container.querySelector('b')).toBeNull();
    expect(screen.getByText('received a message')).toBeInTheDocument();
    expect(screen.getByText('(message no longer available)')).toBeInTheDocument();
  });

  it('loads older pages with the cursor', async () => {
    api.listActivity
      .mockResolvedValueOnce({ items: [item({ id: 1 })], next_cursor: 'c1' })
      .mockResolvedValueOnce({ items: [item({ id: 2, subject: 'Older' })], next_cursor: '' });
    render(ActivityFeed, { projectId: 1 });
    await fireEvent.click(await screen.findByRole('button', { name: 'Load older activity' }));
    expect(await screen.findByText('Older')).toBeInTheDocument();
    expect(api.listActivity).toHaveBeenLastCalledWith(1, 'c1');
  });
});

import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Delivery } from '$lib/types';

const api = vi.hoisted(() => ({ listDeliveries: vi.fn(), releaseMessage: vi.fn() }));
vi.mock('$lib/api', async (orig) => ({ ...(await orig<typeof import('$lib/api')>()), api }));

import MessageDeliveries from './MessageDeliveries.svelte';

const row = (over: Partial<Delivery>): Delivery => ({
  id: 1,
  message_id: 5,
  run: 'auto',
  kind: 'deliver',
  recipient: 'a@corp.example',
  effective_recipient: 'a@corp.example',
  status: 'sent',
  error: '',
  attempts: 1,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
  sent_at: null,
  ...over
});

afterEach(() => vi.restoreAllMocks());

describe('MessageDeliveries', () => {
  it('explains an empty list', async () => {
    api.listDeliveries.mockResolvedValue([]);
    render(MessageDeliveries, { projectId: 1, messageId: 5, canRelease: false });
    expect(await screen.findByText(/Nothing was sent for this message/)).toBeInTheDocument();
  });

  it('shows the status per recipient, redirects and blocks', async () => {
    api.listDeliveries.mockResolvedValue([
      row({ id: 1 }),
      row({ id: 2, kind: 'redirect', recipient: 'x@elsewhere.test', effective_recipient: 'pm@corp.example' }),
      row({ id: 3, kind: 'skip', status: 'skipped', recipient: 'y@elsewhere.test', effective_recipient: '', error: 'not on the allowlist' }),
      row({ id: 4, kind: 'blocked', status: 'blocked', recipient: '', effective_recipient: '', error: 'delivery blocked: no transport' })
    ]);
    render(MessageDeliveries, { projectId: 1, messageId: 5, canRelease: false });
    expect(await screen.findByText('x@elsewhere.test → pm@corp.example')).toBeInTheDocument();
    expect(screen.getByText('Not sent')).toBeInTheDocument();
    expect(screen.getByText('not on the allowlist')).toBeInTheDocument();
    expect(screen.getByText('All recipients')).toBeInTheDocument();
    expect(screen.getByText('Blocked')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Release…' })).not.toBeInTheDocument();
  });

  it('releases to typed addresses after a confirmation and shows a refusal', async () => {
    api.listDeliveries.mockResolvedValue([]);
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
    render(MessageDeliveries, { projectId: 1, messageId: 5, canRelease: true });
    await screen.findByText(/Nothing was sent/);

    await fireEvent.click(screen.getByRole('button', { name: 'Release…' }));
    expect(await screen.findByText('Enter at least one address.')).toBeInTheDocument();

    await fireEvent.input(screen.getByLabelText('Addresses'), { target: { value: 'qa@corp.example, dev@corp.example' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Release…' }));
    expect(confirm).toHaveBeenCalled();
    expect(api.releaseMessage).not.toHaveBeenCalled();

    confirm.mockReturnValue(true);
    api.releaseMessage.mockRejectedValue(new Error('delivery blocked: dev@corp.example is not on the allowlist'));
    await fireEvent.click(screen.getByRole('button', { name: 'Release…' }));
    await waitFor(() => expect(api.releaseMessage).toHaveBeenCalledWith(1, 5, ['qa@corp.example', 'dev@corp.example']));
    expect(await screen.findByText(/is not on the allowlist/)).toBeInTheDocument();
  });
});

import type { ComponentProps } from 'svelte';
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { IssuedToken, Token } from '$lib/types';
import TokenManager from './TokenManager.svelte';

const token = (over: Partial<Token> = {}): Token => ({
  id: 1,
  owner_user_id: 1,
  project_id: null,
  prefix: 'spu_abcd1234',
  label: 'ci',
  scopes: ['read'],
  created_at: '2026-01-01T00:00:00Z',
  revoked_at: null,
  last_used_at: null,
  ...over
});

const base = { id: 't', heading: 'API tokens', intro: 'Intro', scopes: ['read', 'write'] as const };

const show = (props: ComponentProps<typeof TokenManager>) => render(TokenManager, { props });

afterEach(() => vi.restoreAllMocks());

describe('TokenManager', () => {
  it('lists tokens and shows a created secret once, never in the list', async () => {
    const issued: IssuedToken = { ...token({ id: 2, prefix: 'spu_newnewnew1' }), secret: 'spu_SECRETVALUE' };
    const create = vi.fn().mockResolvedValue(issued);
    show({
      ...base,
      scopes: [...base.scopes],
      load: () => Promise.resolve([token()]),
      create,
      revoke: vi.fn()
    });
    expect(await screen.findByText('spu_abcd1234…')).toBeInTheDocument();

    await fireEvent.click(screen.getByLabelText('Read and write (member level)'));
    await fireEvent.click(screen.getByRole('button', { name: 'Create token' }));
    await waitFor(() => expect(create).toHaveBeenCalledWith({ label: '', scopes: ['write'] }));

    expect(await screen.findByTestId('issued-secret')).toHaveTextContent('spu_SECRETVALUE');
    // the list row only has the prefix
    expect(screen.getByText('spu_newnewnew1…')).toBeInTheDocument();

    await fireEvent.click(screen.getByRole('button', { name: 'I have copied it' }));
    expect(screen.queryByText('spu_SECRETVALUE')).not.toBeInTheDocument();
  });

  it('has no create form when creation is not allowed', async () => {
    show({
      ...base,
      scopes: [...base.scopes],
      load: () => Promise.resolve([token()]),
      revoke: vi.fn()
    });
    await screen.findByText('spu_abcd1234…');
    expect(screen.queryByRole('button', { name: 'Create token' })).not.toBeInTheDocument();
  });

  it('revokes after confirmation and marks the row', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    const revoke = vi.fn().mockResolvedValue(undefined);
    show({ ...base, scopes: [...base.scopes], load: () => Promise.resolve([token()]), revoke });
    await fireEvent.click(await screen.findByRole('button', { name: 'Revoke' }));
    await waitFor(() => expect(revoke).toHaveBeenCalledWith(expect.objectContaining({ id: 1 })));
    expect(await screen.findByText('revoked')).toBeInTheDocument();
  });

  it('does not revoke when the confirmation is declined', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(false);
    const revoke = vi.fn();
    show({ ...base, scopes: [...base.scopes], load: () => Promise.resolve([token()]), revoke });
    await fireEvent.click(await screen.findByRole('button', { name: 'Revoke' }));
    expect(revoke).not.toHaveBeenCalled();
  });

  it('shows a load error', async () => {
    show({
      ...base,
      scopes: [...base.scopes],
      load: () => Promise.reject(new Error('forbidden')),
      revoke: vi.fn()
    });
    expect(await screen.findByRole('alert')).toHaveTextContent('forbidden');
  });
});

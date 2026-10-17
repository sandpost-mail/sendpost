import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import PasswordForm from './PasswordForm.svelte';

async function fill(current: string, next: string, again: string) {
  await fireEvent.input(screen.getByLabelText('Current password'), { target: { value: current } });
  await fireEvent.input(screen.getByLabelText('New password'), { target: { value: next } });
  await fireEvent.input(screen.getByLabelText('Repeat the new password'), { target: { value: again } });
  await fireEvent.click(screen.getByRole('button', { name: 'Change password' }));
}

describe('PasswordForm', () => {
  it('rejects a short password and a mismatch', async () => {
    const onchange = vi.fn();
    render(PasswordForm, { onchange });
    await fill('old-password-1', 'short', 'other');
    expect(await screen.findByText('Use at least 12 characters.')).toBeInTheDocument();
    expect(screen.getByText('The two passwords do not match.')).toBeInTheDocument();
    expect(onchange).not.toHaveBeenCalled();
  });

  it('rejects reusing the current password', async () => {
    const onchange = vi.fn();
    render(PasswordForm, { onchange });
    await fill('same-password-12', 'same-password-12', 'same-password-12');
    expect(await screen.findByText(/different from the current one/)).toBeInTheDocument();
    expect(onchange).not.toHaveBeenCalled();
  });

  it('changes the password and reports success', async () => {
    const onchange = vi.fn().mockResolvedValue(undefined);
    const onchanged = vi.fn();
    render(PasswordForm, { onchange, onchanged });
    await fill('old-password-1', 'new-password-123', 'new-password-123');
    await waitFor(() => expect(onchange).toHaveBeenCalledWith('old-password-1', 'new-password-123'));
    expect(await screen.findByRole('status')).toHaveTextContent('Password changed');
    expect(onchanged).toHaveBeenCalled();
    expect(screen.getByLabelText('New password')).toHaveValue('');
  });

  it('shows the server error', async () => {
    const onchange = vi.fn().mockRejectedValue(new Error('current password is wrong'));
    render(PasswordForm, { onchange });
    await fill('old-password-1', 'new-password-123', 'new-password-123');
    expect(await screen.findByRole('alert')).toHaveTextContent('current password is wrong');
  });
});

import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import { ApiError } from '$lib/api';
import LoginForm from './LoginForm.svelte';

async function fill(email: string, password: string) {
  await fireEvent.input(screen.getByLabelText('Email'), { target: { value: email } });
  await fireEvent.input(screen.getByLabelText('Password'), { target: { value: password } });
  await fireEvent.click(screen.getByRole('button', { name: 'Sign in' }));
}

describe('LoginForm', () => {
  it('validates before calling the API', async () => {
    const onlogin = vi.fn();
    render(LoginForm, { onlogin });
    await fill('not-an-email', '');
    expect(await screen.findByText('Enter a valid email address.')).toBeInTheDocument();
    expect(screen.getByText('Enter your password.')).toBeInTheDocument();
    expect(screen.getByLabelText('Email')).toHaveAttribute('aria-invalid', 'true');
    expect(onlogin).not.toHaveBeenCalled();
  });

  it('submits trimmed credentials as typed', async () => {
    const onlogin = vi.fn().mockResolvedValue(undefined);
    render(LoginForm, { onlogin });
    await fill('a@example.test', 'secret-pass');
    await waitFor(() => expect(onlogin).toHaveBeenCalledWith('a@example.test', 'secret-pass'));
  });

  it('shows a clear message for wrong credentials and clears the password', async () => {
    const onlogin = vi.fn().mockRejectedValue(new ApiError(401, 'invalid', 'invalid_credentials'));
    render(LoginForm, { onlogin });
    await fill('a@example.test', 'bad');
    expect(await screen.findByRole('alert')).toHaveTextContent('Wrong email or password.');
    expect(screen.getByLabelText('Password')).toHaveValue('');
  });

  it('explains rate limiting', async () => {
    const onlogin = vi.fn().mockRejectedValue(new ApiError(429, 'slow down', 'too_many_attempts'));
    render(LoginForm, { onlogin });
    await fill('a@example.test', 'bad');
    expect(await screen.findByRole('alert')).toHaveTextContent('Too many failed attempts');
  });
});

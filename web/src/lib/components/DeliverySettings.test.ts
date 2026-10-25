import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { DeliveryPolicy, Transport } from '$lib/types';

const api = vi.hoisted(() => ({
  getTransport: vi.fn(),
  getPolicy: vi.fn(),
  putTransport: vi.fn(),
  putPolicy: vi.fn(),
  testTransport: vi.fn(),
  deleteTransport: vi.fn()
}));
vi.mock('$lib/api', async (orig) => ({ ...(await orig<typeof import('$lib/api')>()), api }));

import DeliverySettings from './DeliverySettings.svelte';

const transport = (over: Partial<Transport> = {}): Transport => ({
  kind: 'smtp',
  host: 'smtp.example.com',
  port: 587,
  username: 'relay-user',
  has_secret: true,
  tls_mode: 'starttls',
  sender: '',
  enabled: false,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
  ...over
});

const policy = (over: Partial<DeliveryPolicy> = {}): DeliveryPolicy => ({
  mode: 'capture',
  allowlist: [],
  redirect_address: '',
  subject_prefix: '',
  configured: false,
  updated_at: '2026-01-01T00:00:00Z',
  ...over
});

beforeEach(() => {
  api.getTransport.mockResolvedValue(null);
  api.getPolicy.mockResolvedValue(policy());
});
afterEach(() => vi.restoreAllMocks());

const show = async () => {
  render(DeliverySettings, { projectId: 7 });
  await screen.findByRole('heading', { name: 'Transport (outbound SMTP relay)' });
};

describe('DeliverySettings', () => {
  it('starts as capture only and says nothing is sent', async () => {
    await show();
    expect(screen.getByLabelText(/Capture only/)).toBeChecked();
    expect(screen.getByText(/nothing is ever sent out/i, { selector: '.warn' })).toBeInTheDocument();
  });

  it('never prints the stored password and keeps it when the connection is unchanged', async () => {
    api.getTransport.mockResolvedValue(transport());
    api.putTransport.mockImplementation(async (_id, input) => transport({ enabled: input.enabled }));
    await show();
    expect(screen.getByLabelText('Password')).toHaveValue('');
    expect(screen.getByLabelText('Password')).toHaveAttribute('type', 'password');
    expect(screen.getByLabelText('Password')).toHaveAttribute('placeholder', expect.stringContaining('stored'));

    await fireEvent.click(screen.getByRole('button', { name: 'Save transport' }));
    await waitFor(() => expect(api.putTransport).toHaveBeenCalled());
    const [, input] = api.putTransport.mock.calls[0];
    expect(input).not.toHaveProperty('secret'); // the stored password is kept server side
    expect(input.enabled).toBe(false);
  });

  it('asks for the password again when the host changes', async () => {
    api.getTransport.mockResolvedValue(transport());
    await show();
    await fireEvent.input(screen.getByLabelText('Host'), { target: { value: 'other.example.net' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Save transport' }));
    expect(await screen.findByText(/Enter the password again/)).toBeInTheDocument();
    expect(api.putTransport).not.toHaveBeenCalled();

    api.putTransport.mockResolvedValue(transport({ host: 'other.example.net' }));
    await fireEvent.input(screen.getByLabelText('Password'), { target: { value: 'new-pass' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Save transport' }));
    await waitFor(() => expect(api.putTransport).toHaveBeenCalled());
    expect(api.putTransport.mock.calls[0][1]).toMatchObject({ host: 'other.example.net', secret: 'new-pass', enabled: false });
  });

  it('validates host and port before calling the API', async () => {
    await show();
    await fireEvent.input(screen.getByLabelText('Host'), { target: { value: 'smtp://bad' } });
    await fireEvent.input(screen.getByLabelText('Port'), { target: { value: '0' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Save transport' }));
    expect(await screen.findByText(/without scheme or port/)).toBeInTheDocument();
    expect(screen.getByText('Port must be between 1 and 65535.')).toBeInTheDocument();
    expect(api.putTransport).not.toHaveBeenCalled();
  });

  it('enabling needs a confirmation', async () => {
    api.getTransport.mockResolvedValue(transport());
    api.putTransport.mockResolvedValue(transport({ enabled: true }));
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
    await show();
    await fireEvent.click(screen.getByRole('button', { name: 'Enable…' }));
    expect(confirm).toHaveBeenCalled();
    expect(api.putTransport).not.toHaveBeenCalled();

    confirm.mockReturnValue(true);
    await fireEvent.click(screen.getByRole('button', { name: 'Enable…' }));
    await waitFor(() => expect(api.putTransport).toHaveBeenCalledWith(7, expect.objectContaining({ enabled: true })));
    expect(await screen.findByRole('button', { name: 'Disable' })).toBeInTheDocument();
  });

  it('shows the result of the connection test', async () => {
    api.getTransport.mockResolvedValue(transport());
    api.testTransport.mockResolvedValue({ ok: false, tls: false, authenticated: false, error: 'tls failed: certificate verification failed' });
    await show();
    await fireEvent.click(screen.getByRole('button', { name: 'Test connection' }));
    expect(await screen.findByText(/certificate verification failed/)).toBeInTheDocument();
  });

  it('warns loudly before real delivery and asks to confirm the policy', async () => {
    api.getTransport.mockResolvedValue(transport({ enabled: true }));
    api.putPolicy.mockResolvedValue(policy({ mode: 'allow', allowlist: ['@corp.example'], configured: true }));
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
    await show();
    await fireEvent.click(screen.getByLabelText(/Allow\b/));
    await fireEvent.input(screen.getByLabelText('Allowlist'), { target: { value: '@corp.example\ndev@x.test' } });
    expect(screen.getByText(/Real email will be delivered/)).toBeInTheDocument();
    expect(screen.getByText(/Whole domains are allowed \(@corp.example\)/)).toBeInTheDocument();

    await fireEvent.click(screen.getByRole('button', { name: 'Save policy' }));
    expect(confirm).toHaveBeenCalledWith(expect.stringContaining('REAL email'));
    expect(api.putPolicy).not.toHaveBeenCalled(); // declined

    confirm.mockReturnValue(true);
    await fireEvent.click(screen.getByRole('button', { name: 'Save policy' }));
    await waitFor(() =>
      expect(api.putPolicy).toHaveBeenCalledWith(7, {
        mode: 'allow',
        allowlist: ['@corp.example', 'dev@x.test'],
        redirect_address: '',
        subject_prefix: ''
      })
    );
  });

  it('saving capture mode needs no confirmation', async () => {
    api.putPolicy.mockResolvedValue(policy({ configured: true }));
    const confirm = vi.spyOn(window, 'confirm');
    await show();
    await fireEvent.click(screen.getByRole('button', { name: 'Save policy' }));
    await waitFor(() => expect(api.putPolicy).toHaveBeenCalled());
    expect(confirm).not.toHaveBeenCalled();
  });

  it('shows a server refusal', async () => {
    api.putPolicy.mockRejectedValue(new Error('redirect_address must be a valid address'));
    await show();
    await fireEvent.click(screen.getByLabelText(/Redirect\b/));
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    await fireEvent.click(screen.getByRole('button', { name: 'Save policy' }));
    expect(await screen.findByText(/redirect_address must be a valid address/)).toBeInTheDocument();
  });
});

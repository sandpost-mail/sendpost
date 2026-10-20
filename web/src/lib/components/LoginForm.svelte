<script lang="ts">
  import { ApiError, errorMessage } from '$lib/api';
  import * as v from '$lib/validate';
  import TextField from './TextField.svelte';

  let { onlogin }: { onlogin: (email: string, password: string) => Promise<void> } = $props();

  let email = $state('');
  let password = $state('');
  let emailError = $state('');
  let passwordError = $state('');
  let error = $state('');
  let busy = $state(false);

  /** Friendly text for the codes the login endpoint can answer with. */
  function loginMessage(e: unknown): string {
    if (e instanceof ApiError) {
      if (e.code === 'invalid_credentials' || e.status === 401) return 'Wrong email or password.';
      if (e.code === 'too_many_attempts' || e.status === 429) {
        return 'Too many failed attempts. Wait a few minutes and try again.';
      }
    }
    return errorMessage(e);
  }

  async function submit(event: SubmitEvent) {
    event.preventDefault();
    emailError = v.email(email);
    passwordError = password ? '' : 'Enter your password.';
    error = '';
    if (emailError || passwordError) return;
    busy = true;
    try {
      await onlogin(email, password);
    } catch (e) {
      error = loginMessage(e);
      password = '';
    } finally {
      busy = false;
    }
  }
</script>

<form class="card stack" onsubmit={submit} novalidate>
  <TextField
    id="login-email"
    label="Email"
    type="email"
    autocomplete="username"
    bind:value={email}
    error={emailError}
    required
  />
  <TextField
    id="login-password"
    label="Password"
    type="password"
    autocomplete="current-password"
    bind:value={password}
    error={passwordError}
    required
  />
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  <div><button class="btn primary" disabled={busy}>{busy ? 'Signing in…' : 'Sign in'}</button></div>
</form>

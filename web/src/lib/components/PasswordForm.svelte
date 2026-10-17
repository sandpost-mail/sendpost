<script lang="ts">
  import { errorMessage } from '$lib/api';
  import * as v from '$lib/validate';
  import TextField from './TextField.svelte';

  let {
    onchange,
    onchanged
  }: {
    /** Performs the change, throws on failure. */
    onchange: (current: string, next: string) => Promise<void>;
    onchanged?: () => void;
  } = $props();

  let current = $state('');
  let next = $state('');
  let confirmation = $state('');
  let currentError = $state('');
  let nextError = $state('');
  let confirmError = $state('');
  let error = $state('');
  let done = $state(false);
  let busy = $state(false);

  async function submit(event: SubmitEvent) {
    event.preventDefault();
    done = false;
    error = '';
    currentError = current ? '' : 'Enter your current password.';
    nextError = v.newPassword(next);
    confirmError = v.passwordConfirmation(next, confirmation);
    if (!nextError && next === current) nextError = 'Choose a password different from the current one.';
    if (currentError || nextError || confirmError) return;
    busy = true;
    try {
      await onchange(current, next);
      current = next = confirmation = '';
      done = true;
      onchanged?.();
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = false;
    }
  }
</script>

<form class="card stack" onsubmit={submit} novalidate>
  <TextField
    id="pw-current"
    label="Current password"
    type="password"
    autocomplete="current-password"
    bind:value={current}
    error={currentError}
    required
  />
  <TextField
    id="pw-new"
    label="New password"
    type="password"
    autocomplete="new-password"
    bind:value={next}
    error={nextError}
    hint="{v.PASSWORD_MIN} to {v.PASSWORD_MAX} characters."
    required
  />
  <TextField
    id="pw-confirm"
    label="Repeat the new password"
    type="password"
    autocomplete="new-password"
    bind:value={confirmation}
    error={confirmError}
    required
  />
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  {#if done}<p role="status" class="ok">Password changed. Your other sessions were signed out.</p>{/if}
  <div><button class="btn primary" disabled={busy}>{busy ? 'Saving…' : 'Change password'}</button></div>
</form>

<style>
  .ok { color: var(--ok); margin: 0; }
</style>

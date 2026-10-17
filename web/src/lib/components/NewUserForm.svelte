<script lang="ts">
  import { errorMessage } from '$lib/api';
  import type { User, UserInput } from '$lib/types';
  import * as v from '$lib/validate';
  import TextField from './TextField.svelte';

  let { oncreate }: { oncreate: (input: UserInput) => Promise<User> } = $props();

  let email = $state('');
  let name = $state('');
  let password = $state('');
  let isAdmin = $state(false);
  let errors = $state({ email: '', name: '', password: '' });
  let error = $state('');
  let created = $state('');
  let busy = $state(false);

  async function submit(event: SubmitEvent) {
    event.preventDefault();
    errors = {
      email: v.email(email),
      name: v.optionalMax(name, 'Name', 200),
      password: v.newPassword(password)
    };
    error = created = '';
    if (v.firstError(errors.email, errors.name, errors.password)) return;
    busy = true;
    try {
      const user = await oncreate({ email: email.trim(), name: name.trim(), password, is_admin: isAdmin });
      created = user.email;
      email = name = password = '';
      isAdmin = false;
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = false;
    }
  }
</script>

<form class="card stack" onsubmit={submit} novalidate aria-labelledby="new-user-heading">
  <h2 id="new-user-heading">New user</h2>
  <TextField id="nu-email" label="Email" type="email" autocomplete="off" bind:value={email} error={errors.email} required />
  <TextField id="nu-name" label="Name" autocomplete="off" maxlength={200} bind:value={name} error={errors.name} />
  <TextField
    id="nu-password"
    label="Initial password"
    type="password"
    autocomplete="new-password"
    bind:value={password}
    error={errors.password}
    hint="{v.PASSWORD_MIN} to {v.PASSWORD_MAX} characters. The user must change it at first sign in."
    required
  />
  <label class="check"><input type="checkbox" bind:checked={isAdmin} /> Instance administrator</label>
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  {#if created}<p role="status" class="ok">Created {created}. Share the initial password through a safe channel.</p>{/if}
  <div><button class="btn primary" disabled={busy}>Create user</button></div>
</form>

<style>
  .check { display: flex; gap: 8px; align-items: center; }
  .ok { color: var(--ok); margin: 0; }
</style>

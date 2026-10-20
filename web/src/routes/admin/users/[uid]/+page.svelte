<script lang="ts">
  import { api, errorMessage } from '$lib/api';
  import TextField from '$lib/components/TextField.svelte';
  import TokenManager from '$lib/components/TokenManager.svelte';
  import { fullDate } from '$lib/format';
  import { session } from '$lib/session.svelte';
  import type { User, UserPatch } from '$lib/types';
  import * as v from '$lib/validate';

  let { data } = $props();

  // svelte-ignore state_referenced_locally
  let user = $state<User>(data.user);
  let name = $state('');
  let nameError = $state('');
  let password = $state('');
  let passwordError = $state('');
  let error = $state('');
  let notice = $state('');
  let busy = $state(false);

  $effect(() => {
    user = data.user;
    name = data.user.name;
    password = '';
    error = notice = '';
  });

  let isSelf = $derived(session.user?.id === user.id);

  async function patch(change: UserPatch, done: string) {
    busy = true;
    error = notice = '';
    try {
      user = await api.updateUser(user.id, change);
      name = user.name;
      notice = done;
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = false;
    }
  }

  function rename(event: SubmitEvent) {
    event.preventDefault();
    nameError = v.optionalMax(name, 'Name', 200);
    if (!nameError) patch({ name: name.trim() }, 'Name saved.');
  }

  async function reset(event: SubmitEvent) {
    event.preventDefault();
    passwordError = v.newPassword(password);
    if (passwordError) return;
    if (!confirm(`Reset the password of ${user.email}? Their sessions end and they must choose a new password.`)) return;
    busy = true;
    error = notice = '';
    try {
      await api.resetPassword(user.id, password);
      password = '';
      user = { ...user, must_change_password: true };
      notice = 'Password reset. Share it through a safe channel.';
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = false;
    }
  }
</script>

<svelte:head><title>{user.email} · Sandpost</title></svelte:head>

<div class="stack">
  <header>
    <p><a href="/admin/users">All users</a></p>
    <h1>{user.email}</h1>
    <p class="row muted">
      Created {fullDate(user.created_at)}
      {#if user.is_admin}<span class="badge">admin</span>{/if}
      {#if user.disabled_at}<span class="badge danger">disabled</span>{/if}
      {#if user.must_change_password}<span class="badge">must change password</span>{/if}
    </p>
  </header>

  {#if error}<p class="error" role="alert">{error}</p>{/if}
  {#if notice}<p role="status" class="ok">{notice}</p>{/if}

  <form class="card stack" onsubmit={rename} novalidate aria-labelledby="profile-heading">
    <h2 id="profile-heading">Profile</h2>
    <TextField id="user-name" label="Name" maxlength={200} bind:value={name} error={nameError} />
    <div><button class="btn" disabled={busy}>Save name</button></div>
  </form>

  <section class="card stack" aria-labelledby="role-heading">
    <h2 id="role-heading">Access</h2>
    {#if isSelf}
      <p class="muted">This is your own account. Ask another administrator to change your role or disable it.</p>
    {:else}
      <div class="row">
        <button
          type="button"
          class="btn"
          disabled={busy}
          onclick={() => patch({ is_admin: !user.is_admin }, user.is_admin ? 'Administrator role removed.' : 'User is now an administrator.')}
        >
          {user.is_admin ? 'Remove administrator role' : 'Make administrator'}
        </button>
        <button
          type="button"
          class="btn"
          class:danger={!user.disabled_at}
          disabled={busy}
          onclick={() => patch({ disabled: !user.disabled_at }, user.disabled_at ? 'User enabled.' : 'User disabled. Sessions and tokens stopped.')}
        >
          {user.disabled_at ? 'Enable user' : 'Disable user'}
        </button>
      </div>
      <p class="muted">The last active administrator can not be demoted or disabled.</p>
    {/if}
  </section>

  {#if !isSelf}
    <form class="card stack" onsubmit={reset} novalidate aria-labelledby="reset-heading">
      <h2 id="reset-heading">Reset password</h2>
      <TextField
        id="user-reset"
        label="New password"
        type="password"
        autocomplete="new-password"
        bind:value={password}
        error={passwordError}
        hint="{v.PASSWORD_MIN} to {v.PASSWORD_MAX} characters. The user must change it at next sign in."
      />
      <div><button class="btn" disabled={busy}>Reset password</button></div>
    </form>
  {/if}

  {#key user.id}
    <TokenManager
      id="user-tokens"
      heading="API tokens"
      intro="Tokens of this user. Administrators can revoke them, creating one is up to the user."
      scopes={['read']}
      load={() => api.listUserTokens(user.id)}
      revoke={(t) => api.revokeUserToken(user.id, t.id)}
    />
  {/key}
</div>

<style>
  .ok { color: var(--ok); margin: 0; }
</style>

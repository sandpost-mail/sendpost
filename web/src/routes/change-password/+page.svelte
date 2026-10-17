<script lang="ts">
  import { goto } from '$app/navigation';
  import PasswordForm from '$lib/components/PasswordForm.svelte';
  import { session } from '$lib/session.svelte';

  // Read once: the flag is cleared by the change itself.
  // svelte-ignore state_referenced_locally
  const forced = session.user?.must_change_password ?? false;

  async function done() {
    if (forced) await goto('/', { replaceState: true, invalidateAll: true });
  }
</script>

<svelte:head><title>Change password · Sandpost</title></svelte:head>

<div class="page">
  <h1>Change your password</h1>
  {#if forced}
    <p class="muted">
      You must choose a new password before you can use Sandpost. This also applies after an
      administrator reset your password.
    </p>
  {/if}
  <PasswordForm onchange={(current, next) => session.changePassword(current, next)} onchanged={done} />
  {#if forced}
    <p><button type="button" class="btn small" onclick={async () => { await session.logout(); await goto('/login'); }}>Sign out</button></p>
  {/if}
</div>

<style>
  .page { max-width: 440px; margin: 48px auto; padding: 0 16px; display: flex; flex-direction: column; gap: 16px; }
</style>

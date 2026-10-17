<script lang="ts">
  import { api } from '$lib/api';
  import PasswordForm from '$lib/components/PasswordForm.svelte';
  import TokenManager from '$lib/components/TokenManager.svelte';
  import { session } from '$lib/session.svelte';
</script>

<svelte:head><title>Account · Sandpost</title></svelte:head>

<div class="page stack">
  <header>
    <h1>Your account</h1>
    <p class="muted">{session.user?.email}{session.user?.is_admin ? ' · administrator' : ''}</p>
  </header>

  <TokenManager
    id="my-tokens"
    heading="API tokens"
    intro="Tokens let scripts and CI call the API as you, with at most your own access. The secret is shown once, at creation. Prefix: spu_."
    scopes={['read', 'write', 'manage']}
    load={() => api.listMyTokens()}
    create={(input) => api.createMyToken(input)}
    revoke={(t) => api.revokeMyToken(t.id)}
  />

  <section class="stack" aria-labelledby="pw-heading">
    <h2 id="pw-heading">Password</h2>
    <PasswordForm onchange={(current, next) => session.changePassword(current, next)} />
  </section>
</div>

<style>
  .page { max-width: 760px; margin: 24px auto; padding: 0 16px; }
</style>

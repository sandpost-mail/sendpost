<script lang="ts">
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { safeNext } from '$lib/guard';
  import { session } from '$lib/session.svelte';
  import LoginForm from '$lib/components/LoginForm.svelte';

  async function login(email: string, password: string) {
    const user = await session.login(email, password);
    // A forced password change is handled by the guard on the next navigation.
    await goto(user.must_change_password ? '/change-password' : safeNext(page.url.searchParams.get('next')), {
      replaceState: true,
      invalidateAll: true
    });
  }
</script>

<svelte:head><title>Sign in · Sandpost</title></svelte:head>

<div class="page">
  <h1>Sign in</h1>
  <LoginForm onlogin={login} />
</div>

<style>
  .page { max-width: 400px; margin: 64px auto; padding: 0 16px; display: flex; flex-direction: column; gap: 16px; }
</style>

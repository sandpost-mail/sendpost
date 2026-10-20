<script lang="ts">
  import { goto } from '$app/navigation';
  import { session } from '$lib/session.svelte';
  import type { User } from '$lib/types';

  let { user }: { user: User } = $props();

  async function logout() {
    await session.logout();
    await goto('/login', { replaceState: true, invalidateAll: true });
  }
</script>

<div class="menu">
  <a href="/account" class="who" title="Your account and API tokens">{user.name || user.email}</a>
  {#if user.is_admin}<span class="badge">admin</span>{/if}
  <button type="button" class="btn small" onclick={logout}>Sign out</button>
</div>

<style>
  .menu { display: flex; align-items: center; gap: 8px; margin-left: auto; }
  .who { color: var(--text); text-decoration: none; max-width: 16rem; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .who:hover { text-decoration: underline; }
</style>

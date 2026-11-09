<script lang="ts">
  import '../app.css';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import ProjectSwitcher from '$lib/components/ProjectSwitcher.svelte';
  import UserMenu from '$lib/components/UserMenu.svelte';
  import { guardRedirect } from '$lib/guard';
  import { projects } from '$lib/projects.svelte';
  import { session } from '$lib/session.svelte';

  let { children } = $props();
  let projectId = $derived(page.params.id);
  let onSettings = $derived(page.url.pathname.endsWith('/settings'));
  let onActivity = $derived(page.url.pathname.endsWith('/activity'));
  let onAudit = $derived(page.url.pathname.endsWith('/audit'));
  let onAdmin = $derived(page.url.pathname.startsWith('/admin'));
  // Whether the user may use the app (signed in, no password change pending).
  let ready = $derived(session.user !== null && !session.user.must_change_password);
  // The owner only links come from the project layout data when one is open.
  let isOwner = $derived(page.data.access?.owner === true);

  $effect(() => {
    if (ready) projects.load();
    else projects.reset();
  });

  // The session can end at any moment (expiry, sign out elsewhere, a forced
  // password change): follow it with the same rule the root load uses.
  $effect(() => {
    if (session.status === 'unknown') return;
    const target = guardRedirect(session.user, page.url.pathname, page.url.search);
    if (target) goto(target, { replaceState: true });
  });
</script>

<svelte:head><title>Sandpost</title></svelte:head>

<div class="app">
  <header class="topbar">
    <a class="brand" href="/">Sandpost</a>
    {#if ready && session.user}
      <ProjectSwitcher />
      {#if projectId}
        <nav aria-label="Project">
          <a href="/projects/{projectId}" aria-current={!onSettings && !onAudit && !onActivity ? 'page' : undefined}>Inbox</a>
          <a href="/projects/{projectId}/activity" aria-current={onActivity ? 'page' : undefined}>Activity</a>
          {#if isOwner}
            <a href="/projects/{projectId}/settings" aria-current={onSettings ? 'page' : undefined}>Settings</a>
            <a href="/projects/{projectId}/audit" aria-current={onAudit ? 'page' : undefined}>Audit</a>
          {/if}
        </nav>
      {/if}
      {#if session.user.is_admin}
        <nav aria-label="Administration">
          <a href="/admin" aria-current={onAdmin ? 'page' : undefined}>Admin</a>
        </nav>
      {/if}
      <UserMenu user={session.user} />
    {/if}
  </header>
  <main>
    {@render children()}
  </main>
</div>

<style>
  .app { display: flex; flex-direction: column; height: 100%; }
  .topbar {
    display: flex; align-items: center; gap: 16px; padding: 8px 16px;
    background: var(--surface); border-bottom: 1px solid var(--border);
  }
  .brand { font-weight: 700; text-decoration: none; color: var(--text); letter-spacing: 0.2px; }
  nav { display: flex; gap: 4px; }
  nav a {
    padding: 4px 10px; border-radius: var(--radius); text-decoration: none; color: var(--muted);
  }
  nav a[aria-current='page'] { background: var(--accent-soft); color: var(--accent); }
  main { flex: 1; min-height: 0; overflow: auto; }
</style>

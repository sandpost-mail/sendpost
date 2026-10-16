<script lang="ts">
  import '../app.css';
  import { page } from '$app/state';
  import { onMount } from 'svelte';
  import ProjectSwitcher from '$lib/components/ProjectSwitcher.svelte';
  import { projects } from '$lib/projects.svelte';

  let { children } = $props();
  let projectId = $derived(page.params.id);
  let onSettings = $derived(page.url.pathname.endsWith('/settings'));

  onMount(() => {
    projects.load();
  });
</script>

<div class="app">
  <header class="topbar">
    <a class="brand" href="/">Sandpost</a>
    <ProjectSwitcher />
    {#if projectId}
      <nav aria-label="Project">
        <a href="/projects/{projectId}" aria-current={!onSettings ? 'page' : undefined}>Inbox</a>
        <a href="/projects/{projectId}/settings" aria-current={onSettings ? 'page' : undefined}>Settings</a>
      </nav>
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

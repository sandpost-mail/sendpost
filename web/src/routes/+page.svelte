<script lang="ts">
  import { goto } from '$app/navigation';
  import NewProjectForm from '$lib/components/NewProjectForm.svelte';
  import { lastProjectId, projects } from '$lib/projects.svelte';

  // Open the last project, or the first one, once the list is known.
  $effect(() => {
    if (!projects.loaded || projects.items.length === 0) return;
    const last = lastProjectId();
    const target = projects.items.find((p) => p.id === last) ?? projects.items[0];
    goto(`/projects/${target.id}`, { replaceState: true });
  });
</script>

<div class="page">
  {#if !projects.loaded}
    <p class="muted">Loading…</p>
  {:else if projects.error}
    <p class="error" role="alert">{projects.error}</p>
  {:else if projects.items.length === 0}
    <h1>Welcome to Sandpost</h1>
    <p class="muted">Create a project, then add SMTP credentials and point your app at this server.</p>
    <NewProjectForm />
  {/if}
</div>

<style>
  .page { max-width: 520px; margin: 48px auto; padding: 0 16px; display: flex; flex-direction: column; gap: 16px; }
</style>

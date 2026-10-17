<script lang="ts">
  import { goto } from '$app/navigation';
  import NewProjectForm from '$lib/components/NewProjectForm.svelte';
  import { lastProjectId, projects } from '$lib/projects.svelte';
  import { session } from '$lib/session.svelte';

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
    {#if session.user?.is_admin}
      <p class="muted">Create a project, then add SMTP credentials and point your app at this server.</p>
      <NewProjectForm />
    {:else}
      <p class="muted">
        You do not have access to any project yet. Ask an administrator to give one of your groups a
        role on a project.
      </p>
    {/if}
  {/if}
</div>

<style>
  .page { max-width: 520px; margin: 48px auto; padding: 0 16px; display: flex; flex-direction: column; gap: 16px; }
</style>

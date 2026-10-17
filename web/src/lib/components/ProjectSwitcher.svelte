<script lang="ts">
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { projects } from '$lib/projects.svelte';
  import { session } from '$lib/session.svelte';

  const NEW = 'new';
  let current = $derived(page.params.id ?? '');

  function change(event: Event) {
    const value = (event.currentTarget as HTMLSelectElement).value;
    goto(value === NEW ? '/projects/new' : `/projects/${value}`);
  }
</script>

<label class="switcher">
  <span class="sr-only">Project</span>
  <select class="input" value={current} onchange={change} disabled={!projects.loaded}>
    {#if !current}
      <option value="" disabled>Select a project</option>
    {/if}
    {#each projects.items as p (p.id)}
      <option value={String(p.id)}>{p.name}</option>
    {/each}
    {#if session.user?.is_admin}
      <option value={NEW}>New project…</option>
    {/if}
  </select>
</label>

<style>
  .switcher select { min-width: 12rem; max-width: 40vw; }
</style>

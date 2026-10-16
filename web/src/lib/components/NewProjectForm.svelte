<script lang="ts">
  import { goto } from '$app/navigation';
  import { api, errorMessage } from '$lib/api';
  import { projects } from '$lib/projects.svelte';

  let name = $state('');
  let busy = $state(false);
  let error = $state('');

  async function submit(event: SubmitEvent) {
    event.preventDefault();
    busy = true;
    error = '';
    try {
      const project = await api.createProject(name);
      projects.add(project);
      await goto(`/projects/${project.id}/settings`);
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = false;
    }
  }
</script>

<form class="card stack" onsubmit={submit}>
  <div class="field">
    <label for="project-name">Project name</label>
    <input id="project-name" class="input" bind:value={name} required maxlength="200" placeholder="Billing API" />
  </div>
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  <div><button class="btn primary" disabled={busy || !name.trim()}>Create project</button></div>
</form>

<script lang="ts">
  import { api, errorMessage } from '$lib/api';
  import TextField from '$lib/components/TextField.svelte';
  import type { Group } from '$lib/types';
  import * as v from '$lib/validate';

  let { data } = $props();

  let groups = $state<Group[]>([]);
  let name = $state('');
  let nameError = $state('');
  let error = $state('');
  let busy = $state(false);

  $effect(() => {
    groups = data.groups;
  });

  async function create(event: SubmitEvent) {
    event.preventDefault();
    nameError = v.groupName(name);
    if (nameError) return;
    busy = true;
    error = '';
    try {
      const group = await api.createGroup(name.trim());
      groups = [...groups, group].sort((a, b) => a.name.localeCompare(b.name));
      name = '';
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = false;
    }
  }
</script>

<svelte:head><title>Groups · Sandpost</title></svelte:head>

<div class="stack">
  <h1>Groups</h1>
  <p class="muted">
    A group collects users. A project gives each group a role (viewer, member or owner) in the
    project settings.
  </p>

  {#if groups.length === 0}
    <p class="muted">No groups yet.</p>
  {:else}
    <table>
      <thead><tr><th>Name</th><th>Members</th></tr></thead>
      <tbody>
        {#each groups as g (g.id)}
          <tr><td><a href="/admin/groups/{g.id}">{g.name}</a></td><td>{g.member_count}</td></tr>
        {/each}
      </tbody>
    </table>
  {/if}

  <form class="card stack" onsubmit={create} novalidate aria-labelledby="new-group-heading">
    <h2 id="new-group-heading">New group</h2>
    <TextField id="group-name" label="Name" maxlength={100} bind:value={name} error={nameError} required />
    {#if error}<p class="error" role="alert">{error}</p>{/if}
    <div><button class="btn primary" disabled={busy}>Create group</button></div>
  </form>
</div>

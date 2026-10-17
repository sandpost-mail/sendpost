<script lang="ts">
  import { onMount } from 'svelte';
  import { api, errorMessage } from '$lib/api';
  import type { Assignment, Group, Role } from '$lib/types';
  import * as v from '$lib/validate';
  import TextField from './TextField.svelte';

  let { projectId, isAdmin }: { projectId: number; isAdmin: boolean } = $props();

  const ROLES: Role[] = ['viewer', 'member', 'owner'];

  let assignments = $state<Assignment[]>([]);
  // Only administrators can list groups. Owners type the id of a group instead.
  let groups = $state<Group[]>([]);
  let loaded = $state(false);
  let groupChoice = $state('');
  let role = $state<Role>('viewer');
  let groupError = $state('');
  let error = $state('');
  let busy = $state(false);

  let assignable = $derived(groups.filter((g) => !assignments.some((a) => a.group_id === g.id)));

  onMount(async () => {
    try {
      const [list, all] = await Promise.all([
        api.listProjectGroups(projectId),
        isAdmin ? api.listGroups() : Promise.resolve([] as Group[])
      ]);
      assignments = list;
      groups = all;
    } catch (e) {
      error = errorMessage(e);
    } finally {
      loaded = true;
    }
  });

  /** Assign `groupId` with `next`, replacing its previous role, then reload the list. */
  async function assign(groupId: number, next: Role) {
    error = '';
    try {
      await api.assignProjectGroup(projectId, groupId, next);
      assignments = await api.listProjectGroups(projectId);
    } catch (e) {
      error = errorMessage(e);
    }
  }

  async function add(event: SubmitEvent) {
    event.preventDefault();
    groupError = isAdmin ? (groupChoice ? '' : 'Choose a group.') : v.id(groupChoice, 'Group id');
    if (groupError) return;
    busy = true;
    try {
      await assign(Number(groupChoice), role);
      if (!error) groupChoice = '';
    } finally {
      busy = false;
    }
  }

  async function remove(a: Assignment) {
    if (!confirm(`Remove ${a.group_name} from this project? Its members lose the ${a.role} role here.`)) return;
    error = '';
    try {
      await api.removeProjectGroup(projectId, a.group_id);
      assignments = assignments.filter((x) => x.group_id !== a.group_id);
    } catch (e) {
      error = errorMessage(e);
    }
  }
</script>

<section class="card stack" aria-labelledby="access-heading">
  <h2 id="access-heading">Access</h2>
  <p class="muted">
    A user gets the highest role among their groups on this project. Viewers read messages, members
    also delete, tag and comment, owners manage the project and its access.
  </p>

  {#if error}<p class="error" role="alert">{error}</p>{/if}

  {#if !loaded}
    <p class="muted">Loading…</p>
  {:else if assignments.length === 0}
    <p class="muted">No group has a role on this project. Only administrators can see it.</p>
  {:else}
    <table>
      <thead><tr><th>Group</th><th>Role</th><th></th></tr></thead>
      <tbody>
        {#each assignments as a (a.group_id)}
          <tr>
            <td>{a.group_name}</td>
            <td>
              <select
                class="input"
                aria-label="Role of {a.group_name}"
                value={a.role}
                onchange={(e) => assign(a.group_id, e.currentTarget.value as Role)}
              >
                {#each ROLES as r (r)}<option value={r}>{r}</option>{/each}
              </select>
            </td>
            <td class="actions"><button type="button" class="btn small danger" onclick={() => remove(a)}>Remove</button></td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}

  <form class="row add" onsubmit={add} novalidate>
    {#if isAdmin}
      <div class="field">
        <label for="access-group">Group</label>
        <select id="access-group" class="input" bind:value={groupChoice} aria-invalid={groupError ? 'true' : undefined}>
          <option value="">Choose a group</option>
          {#each assignable as g (g.id)}<option value={String(g.id)}>{g.name}</option>{/each}
        </select>
        {#if groupError}<span class="field-error" role="alert">{groupError}</span>{/if}
      </div>
    {:else}
      <TextField
        id="access-group"
        label="Group id"
        inputmode="numeric"
        hint="Owners can assign any existing group by its id."
        bind:value={groupChoice}
        error={groupError}
      />
    {/if}
    <div class="field">
      <label for="access-role">Role</label>
      <select id="access-role" class="input" bind:value={role}>
        {#each ROLES as r (r)}<option value={r}>{r}</option>{/each}
      </select>
    </div>
    <button class="btn primary" disabled={busy}>Assign role</button>
  </form>
</section>

<style>
  .add { align-items: flex-start; }
  .add button { align-self: flex-end; }
  .add :global(.field), .add .field { min-width: 10rem; }
  .actions { text-align: right; }
  .field-error { color: var(--danger); font-size: 0.85em; }
</style>

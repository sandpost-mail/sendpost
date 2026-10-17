<script lang="ts">
  import { goto } from '$app/navigation';
  import { api, errorMessage } from '$lib/api';
  import TextField from '$lib/components/TextField.svelte';
  import type { Group, User } from '$lib/types';
  import * as v from '$lib/validate';

  let { data } = $props();

  // svelte-ignore state_referenced_locally
  let group = $state<Group>(data.group);
  let members = $state<User[]>([]);
  let name = $state('');
  let nameError = $state('');
  let candidate = $state('');
  let error = $state('');
  let notice = $state('');
  let busy = $state(false);

  $effect(() => {
    group = data.group;
    members = data.members;
    name = data.group.name;
    candidate = '';
    error = notice = '';
  });

  // Disabled users can not sign in, so they are not offered.
  let addable = $derived(
    data.users.filter((u) => !u.disabled_at && !members.some((m) => m.id === u.id))
  );

  /** Run one change, showing its error. Returns whether it worked. */
  async function run(action: () => Promise<void>): Promise<boolean> {
    busy = true;
    error = notice = '';
    try {
      await action();
      return true;
    } catch (e) {
      error = errorMessage(e);
      return false;
    } finally {
      busy = false;
    }
  }

  async function rename(event: SubmitEvent) {
    event.preventDefault();
    nameError = v.groupName(name);
    if (nameError) return;
    await run(async () => {
      group = { ...group, ...(await api.renameGroup(group.id, name.trim())) };
      notice = 'Group renamed.';
    });
  }

  async function add(event: SubmitEvent) {
    event.preventDefault();
    const user = addable.find((u) => String(u.id) === candidate);
    if (!user) return;
    await run(async () => {
      await api.addGroupMember(group.id, user.id);
      members = [...members, user].sort((a, b) => a.email.localeCompare(b.email));
      candidate = '';
    });
  }

  async function remove(user: User) {
    await run(async () => {
      await api.removeGroupMember(group.id, user.id);
      members = members.filter((m) => m.id !== user.id);
    });
  }

  async function destroy() {
    if (!confirm(`Delete ${group.name}? Its members lose the access it gave them on projects.`)) return;
    if (await run(() => api.deleteGroup(group.id))) await goto('/admin/groups');
  }
</script>

<svelte:head><title>{group.name} · Sandpost</title></svelte:head>

<div class="stack">
  <header>
    <p><a href="/admin/groups">All groups</a></p>
    <h1>{group.name}</h1>
  </header>

  {#if error}<p class="error" role="alert">{error}</p>{/if}
  {#if notice}<p role="status" class="ok">{notice}</p>{/if}

  <form class="card stack" onsubmit={rename} novalidate aria-labelledby="rename-heading">
    <h2 id="rename-heading">Name</h2>
    <TextField id="group-rename" label="Group name" maxlength={100} bind:value={name} error={nameError} required />
    <div><button class="btn" disabled={busy}>Rename</button></div>
  </form>

  <section class="card stack" aria-labelledby="members-heading">
    <h2 id="members-heading">Members ({members.length})</h2>
    {#if members.length === 0}
      <p class="muted">No members yet.</p>
    {:else}
      <table>
        <thead><tr><th>Email</th><th>Name</th><th></th></tr></thead>
        <tbody>
          {#each members as m (m.id)}
            <tr>
              <td>{m.email}</td>
              <td>{m.name || '-'}</td>
              <td class="actions">
                <button type="button" class="btn small danger" disabled={busy} onclick={() => remove(m)}>Remove</button>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
    <form class="row" onsubmit={add}>
      <label class="sr-only" for="member-candidate">User to add</label>
      <select id="member-candidate" class="input" bind:value={candidate}>
        <option value="">Choose a user</option>
        {#each addable as u (u.id)}<option value={String(u.id)}>{u.email}</option>{/each}
      </select>
      <button class="btn primary" disabled={busy || !candidate}>Add member</button>
    </form>
  </section>

  <section class="card stack" aria-labelledby="danger-heading">
    <h2 id="danger-heading">Delete group</h2>
    <p class="muted">Removes the group, its memberships and its role on every project.</p>
    <div><button type="button" class="btn danger" disabled={busy} onclick={destroy}>Delete group</button></div>
  </section>
</div>

<style>
  .ok { color: var(--ok); margin: 0; }
  .actions { text-align: right; }
</style>

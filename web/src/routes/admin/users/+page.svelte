<script lang="ts">
  import { api } from '$lib/api';
  import NewUserForm from '$lib/components/NewUserForm.svelte';
  import { fullDate } from '$lib/format';
  import type { User, UserInput } from '$lib/types';

  let { data } = $props();

  let users = $state<User[]>([]);
  $effect(() => {
    users = data.users;
  });

  async function create(input: UserInput): Promise<User> {
    const user = await api.createUser(input);
    users = [...users, user].sort((a, b) => a.email.localeCompare(b.email));
    return user;
  }
</script>

<svelte:head><title>Users · Sandpost</title></svelte:head>

<div class="stack">
  <h1>Users</h1>

  <table>
    <thead><tr><th>Email</th><th>Name</th><th>Status</th><th>Created</th></tr></thead>
    <tbody>
      {#each users as u (u.id)}
        <tr class:disabled={u.disabled_at}>
          <td><a href="/admin/users/{u.id}">{u.email}</a></td>
          <td>{u.name || '-'}</td>
          <td class="row">
            {#if u.is_admin}<span class="badge">admin</span>{/if}
            {#if u.disabled_at}<span class="badge danger">disabled</span>{/if}
            {#if u.must_change_password}<span class="badge">must change password</span>{/if}
          </td>
          <td>{fullDate(u.created_at)}</td>
        </tr>
      {/each}
    </tbody>
  </table>

  <NewUserForm oncreate={create} />
</div>

<style>
  tr.disabled td { color: var(--muted); }
</style>

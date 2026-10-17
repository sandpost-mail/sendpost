<script lang="ts">
  import { onMount } from 'svelte';
  import { errorMessage } from '$lib/api';
  import { fullDate, relativeTime } from '$lib/format';
  import type { AuditEvent, AuditPage } from '$lib/types';

  let {
    load,
    showProject = false
  }: {
    /** Fetch one page, an empty cursor means the first. */
    load: (cursor: string) => Promise<AuditPage>;
    showProject?: boolean;
  } = $props();

  let events = $state<AuditEvent[]>([]);
  let cursor = $state('');
  let loading = $state(false);
  let loaded = $state(false);
  let error = $state('');

  async function more() {
    if (loading) return;
    loading = true;
    error = '';
    try {
      const page = await load(cursor);
      events = [...events, ...page.items];
      cursor = page.next_cursor;
    } catch (e) {
      error = errorMessage(e);
    } finally {
      loading = false;
      loaded = true;
    }
  }

  onMount(more);

  /** Details never hold secrets (server side rule), show them as compact JSON. */
  const detailText = (e: AuditEvent) =>
    e.detail && Object.keys(e.detail).length > 0 ? JSON.stringify(e.detail) : '';
</script>

{#if error}<p class="error" role="alert">{error}</p>{/if}

{#if loaded && events.length === 0 && !error}
  <p class="muted">No audit events yet.</p>
{:else if events.length > 0}
  <table>
    <thead>
      <tr>
        <th>When</th><th>Who</th><th>Action</th><th>Target</th>
        {#if showProject}<th>Project</th>{/if}
        <th>Detail</th>
      </tr>
    </thead>
    <tbody>
      {#each events as e (e.id)}
        <tr>
          <td title={fullDate(e.at)}>{relativeTime(e.at)}</td>
          <td>{e.actor_email || 'system'}</td>
          <td class="mono">{e.action}</td>
          <td class="mono">{e.target}</td>
          {#if showProject}<td>{e.project_id ?? '-'}</td>{/if}
          <td class="mono detail">{detailText(e)}</td>
        </tr>
      {/each}
    </tbody>
  </table>
{/if}

{#if loading}<p class="muted">Loading…</p>{/if}
{#if cursor && !loading}
  <div><button type="button" class="btn" onclick={more}>Load older events</button></div>
{/if}

<style>
  .detail { overflow-wrap: anywhere; max-width: 28rem; }
</style>

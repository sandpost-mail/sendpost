<script lang="ts">
  import { onMount } from 'svelte';
  import { api, errorMessage } from '$lib/api';
  import { fullDate, relativeTime } from '$lib/format';
  import type { ActivityItem } from '$lib/types';

  let { projectId }: { projectId: number } = $props();

  let items = $state<ActivityItem[]>([]);
  let cursor = $state('');
  let loading = $state(false);
  let loaded = $state(false);
  let error = $state('');

  async function more() {
    if (loading) return;
    loading = true;
    error = '';
    try {
      const page = await api.listActivity(projectId, cursor);
      items = [...items, ...page.items];
      cursor = page.next_cursor;
    } catch (e) {
      error = errorMessage(e);
    } finally {
      loading = false;
      loaded = true;
    }
  }

  onMount(more);

  const verb = (i: ActivityItem) =>
    i.kind === 'message' ? 'received a message' : i.kind === 'comment' ? 'commented' : 'released a message';
  const key = (i: ActivityItem) => `${i.kind}:${i.id}`;
</script>

{#if error}<p class="error" role="alert">{error}</p>{/if}

{#if loaded && items.length === 0 && !error}
  <p class="muted">Nothing has happened in this project yet.</p>
{:else if items.length > 0}
  <ul class="feed" aria-label="Activity">
    {#each items as i (key(i))}
      <li>
        <span class="muted when" title={fullDate(i.at)}>{relativeTime(i.at)}</span>
        <span>
          {#if i.kind !== 'message' && i.actor}<strong>{i.actor}</strong>{/if}
          {#if i.kind === 'message'}<strong>{i.actor || '(unknown sender)'}</strong>{/if}
          {verb(i)}
          {#if i.message_id > 0}
            <a href="/projects/{projectId}/messages/{i.message_id}">{i.subject || '(no subject)'}</a>
          {:else}
            <span class="muted">(message no longer available)</span>
          {/if}
        </span>
        {#if i.detail}<span class="detail muted">{i.detail}</span>{/if}
      </li>
    {/each}
  </ul>
{/if}

{#if loading}<p class="muted">Loading…</p>{/if}
{#if cursor && !loading}
  <div><button type="button" class="btn" onclick={more}>Load older activity</button></div>
{/if}

<style>
  .feed { list-style: none; padding: 0; margin: 0; display: flex; flex-direction: column; gap: 8px; }
  .feed li { display: grid; grid-template-columns: 9rem 1fr; gap: 2px 12px; padding: 6px 0; border-bottom: 1px solid var(--border); }
  .detail { grid-column: 2; overflow-wrap: anywhere; white-space: pre-wrap; }
</style>

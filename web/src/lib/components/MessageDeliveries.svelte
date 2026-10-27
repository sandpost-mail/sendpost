<script lang="ts">
  import { onMount } from 'svelte';
  import { api, errorMessage } from '$lib/api';
  import { parseAllowlist, statusText } from '$lib/delivery';
  import { relativeTime, fullDate } from '$lib/format';
  import type { Delivery } from '$lib/types';

  let { projectId, messageId, canRelease }: { projectId: number; messageId: number; canRelease: boolean } = $props();

  let rows = $state<Delivery[]>([]);
  let loaded = $state(false);
  let error = $state('');
  let target = $state('');
  let releaseError = $state('');
  let busy = $state(false);

  async function load() {
    try {
      rows = await api.listDeliveries(projectId, messageId);
      error = '';
    } catch (e) {
      error = errorMessage(e);
    } finally {
      loaded = true;
    }
  }

  onMount(() => {
    load();
    // Rows change while a job runs: refresh while something is pending.
    const timer = setInterval(() => {
      if (rows.some((r) => r.status === 'pending')) load();
    }, 3000);
    return () => clearInterval(timer);
  });

  async function release(event: SubmitEvent) {
    event.preventDefault();
    releaseError = '';
    const to = parseAllowlist(target);
    if (to.length === 0) {
      releaseError = 'Enter at least one address.';
      return;
    }
    if (!confirm(`Send this message for real to:\n${to.join('\n')}`)) return;
    busy = true;
    try {
      await api.releaseMessage(projectId, messageId, to);
      target = '';
      await load();
    } catch (e) {
      releaseError = errorMessage(e);
    } finally {
      busy = false;
    }
  }

  const label = (r: Delivery) =>
    r.recipient === '' ? 'All recipients' : r.kind === 'redirect' ? `${r.recipient} → ${r.effective_recipient}` : r.recipient;
  const kindText = (r: Delivery) => (r.run.startsWith('release:') ? 'release' : r.kind === 'blocked' ? 'policy' : r.kind);
</script>

<div class="stack">
  <p class="muted">
    Outbound delivery of this message. Captured mail is never removed, this is only what was sent on and what was not.
  </p>
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  {#if !loaded}
    <p class="muted">Loading…</p>
  {:else if rows.length === 0}
    <p class="muted">Nothing was sent for this message (capture only, or no policy applied).</p>
  {:else}
    <table>
      <thead><tr><th>Recipient</th><th>Kind</th><th>Status</th><th>Detail</th><th>Updated</th></tr></thead>
      <tbody>
        {#each rows as r (r.id)}
          <tr>
            <td class="mono wrap">{label(r)}</td>
            <td>{kindText(r)}</td>
            <td><span class="badge" class:danger={r.status === 'failed' || r.status === 'blocked'}>{statusText(r.status)}</span></td>
            <td class="wrap">{r.error}{#if r.attempts > 1} <span class="muted">({r.attempts} attempts)</span>{/if}</td>
            <td title={fullDate(r.updated_at)}>{relativeTime(r.updated_at)}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}

  {#if canRelease}
    <form class="stack" onsubmit={release} novalidate aria-label="Release message">
      <h3>Release</h3>
      <p class="muted">
        Send this message to specific addresses. Every address must be on the project allowlist, and the transport must
        be enabled. The original recipients are not contacted and Bcc is not sent.
      </p>
      <label class="sr-only" for="release-to">Addresses</label>
      <textarea id="release-to" class="input" rows="2" bind:value={target} placeholder="qa@example.com"></textarea>
      <div><button class="btn danger" disabled={busy}>Release…</button></div>
      {#if releaseError}<p class="error" role="alert">{releaseError}</p>{/if}
    </form>
  {/if}
</div>

<style>
  .wrap { overflow-wrap: anywhere; }
  textarea.input { font-family: var(--mono); width: 100%; }
</style>

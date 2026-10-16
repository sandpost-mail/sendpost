<script lang="ts">
  import { getContext } from 'svelte';
  import { api, errorMessage, messageUrl } from '$lib/api';
  import { bytes, fullDate } from '$lib/format';
  import type { Inbox } from '$lib/inbox.svelte';
  import type { MessageDetail } from '$lib/types';

  let { data } = $props();
  const inbox = getContext<Inbox>('inbox');

  type Tab = 'html' | 'text' | 'source' | 'headers' | 'attachments' | 'links';

  let message = $derived<MessageDetail>(data.message);
  let tab = $state<Tab>('text');
  let viewport = $state<'desktop' | 'mobile'>('desktop');
  let source = $state<{ id: number; text: string } | null>(null);
  let sourceError = $state('');

  // New message: pick the best default tab, mark it read.
  $effect(() => {
    const m = data.message;
    tab = m.has_html ? 'html' : 'text';
    source = null;
    sourceError = '';
    if (!m.read_at) {
      api
        .markRead(data.projectId, m.id)
        .then(() => inbox?.markRead(m.id))
        .catch(() => {});
    }
  });

  $effect(() => {
    if (tab !== 'source' || source?.id === message.id) return;
    const id = message.id;
    api
      .rawMessage(data.projectId, id)
      .then((text) => (source = { id, text }))
      .catch((e) => (sourceError = errorMessage(e)));
  });

  const tabs = $derived<{ id: Tab; label: string }[]>([
    ...(message.has_html ? [{ id: 'html' as const, label: 'HTML' }] : []),
    { id: 'text', label: 'Text' },
    { id: 'source', label: 'Source' },
    { id: 'headers', label: `Headers (${Object.keys(message.headers).length})` },
    { id: 'attachments', label: `Attachments (${message.attachments.length})` },
    { id: 'links', label: `Links (${message.links.length})` }
  ]);

  const kinds = ['to', 'cc', 'bcc', 'envelope'] as const;
  const byKind = (kind: string) => message.recipients.filter((r) => r.kind === kind);
  const url = (suffix: string) => `${messageUrl(data.projectId, message.id)}${suffix}`;
</script>

<article class="message">
  <header class="head">
    <a class="btn small back" href="/projects/{data.projectId}">Back</a>
    <h1>{message.subject || '(no subject)'}</h1>
    <dl>
      <dt>From</dt><dd>{message.from || '(unknown)'}</dd>
      {#each kinds as kind (kind)}
        {#if byKind(kind).length > 0}
          <dt>{kind === 'envelope' ? 'Envelope' : kind.toUpperCase()}</dt>
          <dd>{byKind(kind).map((r) => r.address).join(', ')}</dd>
        {/if}
      {/each}
      <dt>Date</dt><dd>{fullDate(message.received_at)} · {bytes(message.size_bytes)}</dd>
    </dl>
    {#if message.tags.length > 0}
      <div class="row">{#each message.tags as tag (tag)}<span class="badge">{tag}</span>{/each}</div>
    {/if}
    <div class="row actions">
      <a class="btn small" href={url('/raw?download=1')}>Download .eml</a>
    </div>
  </header>

  <div class="tabs" role="tablist" aria-label="Message views">
    {#each tabs as t (t.id)}
      <button type="button" role="tab" aria-selected={tab === t.id} class:active={tab === t.id} onclick={() => (tab = t.id)}>
        {t.label}
      </button>
    {/each}
  </div>

  <div class="panel" role="tabpanel">
    {#if tab === 'html'}
      <div class="row viewport">
        <button type="button" class="btn small" class:primary={viewport === 'desktop'} onclick={() => (viewport = 'desktop')}>Desktop</button>
        <button type="button" class="btn small" class:primary={viewport === 'mobile'} onclick={() => (viewport = 'mobile')}>Mobile</button>
        <span class="muted">Scripts and remote content are blocked.</span>
      </div>
      <div class="frame" class:mobile={viewport === 'mobile'}>
        {#key message.id}
          <iframe
            title="HTML preview"
            src={url('/html')}
            sandbox="allow-popups allow-popups-to-escape-sandbox"
            referrerpolicy="no-referrer"
          ></iframe>
        {/key}
      </div>
    {:else if tab === 'text'}
      {#if message.text}
        <pre class="block">{message.text}</pre>
      {:else}
        <p class="muted">This message has no text content.</p>
      {/if}
    {:else if tab === 'source'}
      {#if sourceError}
        <p class="error" role="alert">{sourceError}</p>
      {:else if source?.id === message.id}
        <pre class="block mono">{source.text}</pre>
      {:else}
        <p class="muted">Loading…</p>
      {/if}
    {:else if tab === 'headers'}
      <table>
        <thead><tr><th>Name</th><th>Value</th></tr></thead>
        <tbody>
          {#each Object.entries(message.headers) as [name, values] (name)}
            {#each values as value, i (i)}
              <tr><td class="mono">{name}</td><td class="mono wrap">{value}</td></tr>
            {/each}
          {/each}
        </tbody>
      </table>
    {:else if tab === 'attachments'}
      {#if message.attachments.length === 0}
        <p class="muted">No attachments.</p>
      {:else}
        <table>
          <thead><tr><th>File</th><th>Type</th><th>Size</th><th></th></tr></thead>
          <tbody>
            {#each message.attachments as a (a.ordinal)}
              <tr>
                <td>{a.filename || '(unnamed)'} {#if a.inline}<span class="badge">inline</span>{/if}</td>
                <td class="mono">{a.content_type}</td>
                <td>{bytes(a.size_bytes)}</td>
                <td><a class="btn small" href={url(`/attachments/${a.ordinal}`)}>Download</a></td>
              </tr>
            {/each}
          </tbody>
        </table>
      {/if}
    {:else if tab === 'links'}
      {#if message.links.length === 0}
        <p class="muted">No links found.</p>
      {:else}
        <ul class="links">
          {#each message.links as link (link)}
            <li><a href={link} target="_blank" rel="noopener noreferrer" class="mono wrap">{link}</a></li>
          {/each}
        </ul>
      {/if}
    {/if}
  </div>
</article>

<style>
  .message { display: flex; flex-direction: column; min-height: 100%; }
  .head { padding: 16px; display: flex; flex-direction: column; gap: 8px; background: var(--surface); border-bottom: 1px solid var(--border); }
  .back { display: none; align-self: flex-start; }
  dl { display: grid; grid-template-columns: max-content 1fr; gap: 2px 12px; margin: 0; }
  dt { color: var(--muted); }
  dd { margin: 0; overflow-wrap: anywhere; }
  .tabs { display: flex; gap: 2px; padding: 0 12px; background: var(--surface); border-bottom: 1px solid var(--border); overflow-x: auto; }
  .tabs button {
    padding: 8px 12px; border: 0; background: transparent; color: var(--muted);
    border-bottom: 2px solid transparent; font: inherit; cursor: pointer; white-space: nowrap;
  }
  .tabs button.active { color: var(--accent); border-bottom-color: var(--accent); }
  .panel { padding: 16px; flex: 1; display: flex; flex-direction: column; gap: 12px; min-height: 0; }
  .frame { flex: 1; min-height: 480px; display: flex; justify-content: center; background: var(--surface-2); border-radius: var(--radius); padding: 8px; }
  .frame iframe { width: 100%; height: 100%; min-height: 480px; border: 0; background: #fff; border-radius: 4px; }
  .frame.mobile iframe { width: 375px; max-width: 100%; }
  .wrap { overflow-wrap: anywhere; }
  .links { list-style: none; padding: 0; margin: 0; display: flex; flex-direction: column; gap: 6px; }
  @media (max-width: 800px) { .back { display: inline-flex; } }
</style>

<script lang="ts">
  import { page } from '$app/state';
  import { onMount, setContext, tick, type Snippet } from 'svelte';
  import { Inbox } from '$lib/inbox.svelte';
  import { subscribeToProject, type LiveStatus } from '$lib/live';
  import { relativeTime } from '$lib/format';

  let { projectId, children }: { projectId: number; children: Snippet } = $props();

  // The parent keys this component on projectId, so the id is stable here.
  // svelte-ignore state_referenced_locally
  const pid = projectId;
  const inbox = new Inbox(pid);
  setContext('inbox', inbox);

  let status = $state<LiveStatus>('connecting');
  let search = $state('');
  let sentinel = $state<HTMLElement | null>(null);
  let listEl = $state<HTMLElement | null>(null);
  let activeId = $derived(page.params.mid ? Number(page.params.mid) : null);
  let hasDetail = $derived(activeId !== null);
  let debounce: ReturnType<typeof setTimeout>;

  onMount(() => {
    inbox.reload('');
    return subscribeToProject(pid, {
      onStatus: (s) => (status = s),
      onEvent: () => inbox.refreshHead()
    });
  });

  function onSearch() {
    clearTimeout(debounce);
    debounce = setTimeout(() => inbox.reload(search), 250);
  }

  // Infinite scroll: load while the sentinel is near the viewport of the list.
  function maybeLoadMore() {
    if (!sentinel || !listEl || inbox.loading || inbox.done) return;
    const gap = sentinel.getBoundingClientRect().top - listEl.getBoundingClientRect().bottom;
    if (gap < 200) inbox.loadMore();
  }

  $effect(() => {
    if (!sentinel || !listEl) return;
    const observer = new IntersectionObserver(maybeLoadMore, { root: listEl, rootMargin: '200px' });
    observer.observe(sentinel);
    return () => observer.disconnect();
  });

  // A short first page may leave the sentinel visible with nothing to trigger it.
  $effect(() => {
    void inbox.items.length;
    void inbox.loading;
    tick().then(maybeLoadMore);
  });

  const statusLabel: Record<LiveStatus, string> = {
    connecting: 'Connecting',
    live: 'Live',
    offline: 'Reconnecting'
  };
</script>

<div class="shell" class:has-detail={hasDetail}>
  <aside class="list" aria-label="Messages">
    <div class="toolbar">
      <input
        class="input"
        type="search"
        placeholder="Search: from:alice subject:welcome tag:run-42 after:2026-01-01"
        aria-label="Search messages"
        bind:value={search}
        oninput={onSearch}
      />
      <span class="status" data-status={status} title="Live updates">{statusLabel[status]}</span>
    </div>

    <div class="scroll" bind:this={listEl}>
      {#if inbox.error}<p class="error" role="alert">{inbox.error}</p>{/if}

      <ul>
        {#each inbox.items as m (m.id)}
          <li class:fresh={inbox.fresh.includes(m.id)}>
            <a
              href="/projects/{pid}/messages/{m.id}"
              class:active={m.id === activeId}
              class:unread={!m.read_at}
              aria-current={m.id === activeId ? 'page' : undefined}
            >
              <span class="line1">
                <span class="from">{m.from || '(no sender)'}</span>
                <time datetime={m.received_at} title={new Date(m.received_at).toLocaleString()}>
                  {relativeTime(m.received_at)}
                </time>
              </span>
              <span class="subject">{m.subject || '(no subject)'}</span>
              <span class="meta">
                <span class="to">{m.to.join(', ')}</span>
                {#if m.attachment_count > 0}<span class="badge" title="Attachments">{m.attachment_count} file{m.attachment_count > 1 ? 's' : ''}</span>{/if}
                {#each m.tags as tag (tag)}<span class="badge">{tag}</span>{/each}
              </span>
            </a>
          </li>
        {/each}
      </ul>

      {#if inbox.items.length === 0 && !inbox.loading && !inbox.error}
        <p class="empty muted">
          {inbox.query ? 'No message matches this search.' : 'No messages yet. Send one through SMTP to see it here.'}
        </p>
      {/if}
      {#if inbox.loading}<p class="muted pad">Loading…</p>{/if}
      <div bind:this={sentinel} aria-hidden="true"></div>
    </div>
  </aside>

  <section class="detail">
    {@render children()}
  </section>
</div>

<style>
  .shell { display: grid; grid-template-columns: minmax(280px, 380px) 1fr; height: 100%; }
  .list { display: flex; flex-direction: column; min-height: 0; border-right: 1px solid var(--border); background: var(--surface); }
  .toolbar { display: flex; gap: 8px; align-items: center; padding: 8px; border-bottom: 1px solid var(--border); }
  .toolbar .input { flex: 1; }
  .status { font-size: 0.8em; color: var(--muted); display: inline-flex; align-items: center; gap: 4px; white-space: nowrap; }
  .status::before { content: ''; width: 8px; height: 8px; border-radius: 50%; background: var(--muted); }
  .status[data-status='live']::before { background: var(--ok); }
  .status[data-status='offline']::before { background: var(--danger); }
  .scroll { flex: 1; overflow-y: auto; }
  ul { list-style: none; margin: 0; padding: 0; }
  li { border-bottom: 1px solid var(--border); }
  li.fresh { animation: flash 4s ease-out; }
  @keyframes flash { from { background: var(--fresh); } to { background: transparent; } }
  li a {
    display: flex; flex-direction: column; gap: 2px; padding: 10px 12px;
    color: inherit; text-decoration: none; border-left: 3px solid transparent;
  }
  li a:hover { background: var(--surface-2); }
  li a.active { background: var(--accent-soft); border-left-color: var(--accent); }
  .line1 { display: flex; justify-content: space-between; gap: 8px; }
  .from { font-weight: 500; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .unread .from, .unread .subject { font-weight: 700; }
  time { color: var(--muted); font-size: 0.85em; white-space: nowrap; }
  .subject { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .meta { display: flex; gap: 6px; align-items: center; flex-wrap: wrap; color: var(--muted); font-size: 0.85em; }
  .to { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 100%; }
  .empty, .pad { padding: 16px; }
  .detail { min-width: 0; min-height: 0; overflow: auto; }

  @media (max-width: 800px) {
    .shell { grid-template-columns: 1fr; }
    .shell.has-detail .list { display: none; }
    .shell:not(.has-detail) .detail { display: none; }
  }
</style>

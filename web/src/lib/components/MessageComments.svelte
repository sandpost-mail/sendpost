<script lang="ts">
  import { onMount, untrack } from 'svelte';
  import { api, errorMessage } from '$lib/api';
  import { COMMENT_MAX, commentLength, validateComment } from '$lib/comments';
  import { fullDate, relativeTime } from '$lib/format';
  import type { Comment } from '$lib/types';

  let {
    projectId,
    messageId,
    canComment,
    liveVersion = 0
  }: { projectId: number; messageId: number; canComment: boolean; liveVersion?: number } = $props();

  let items = $state<Comment[]>([]);
  let loaded = $state(false);
  let error = $state('');
  let text = $state('');
  let formError = $state('');
  let busy = $state(false);

  async function loadAll(): Promise<void> {
    try {
      let next = '';
      const all: Comment[] = [];
      do {
        const page = await api.listComments(projectId, messageId, next);
        all.push(...page.items);
        next = page.next_cursor;
      } while (next);
      items = all;
      error = '';
    } catch (e) {
      error = errorMessage(e);
    } finally {
      loaded = true;
    }
  }

  onMount(loadAll);

  // A live event (new comment from someone else, reconnect) reloads the thread.
  let seen = untrack(() => liveVersion);
  $effect(() => {
    if (liveVersion !== seen) {
      seen = liveVersion;
      void loadAll();
    }
  });

  async function submit(event: SubmitEvent) {
    event.preventDefault();
    formError = validateComment(text);
    if (formError) return;
    busy = true;
    try {
      const created = await api.createComment(projectId, messageId, text);
      if (!items.some((c) => c.id === created.id)) items = [...items, created];
      text = '';
    } catch (e) {
      formError = errorMessage(e);
    } finally {
      busy = false;
    }
  }
</script>

<div class="stack">
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  {#if !loaded}
    <p class="muted">Loading…</p>
  {:else if items.length === 0 && !error}
    <p class="muted">No comments yet.</p>
  {:else}
    <ul class="thread" aria-label="Comments">
      {#each items as c (c.id)}
        <li>
          <div class="meta">
            <strong>{c.author}</strong>
            <span class="muted" title={fullDate(c.created_at)}>{relativeTime(c.created_at)}</span>
          </div>
          <!-- Plain text on purpose: the text is never rendered as HTML. -->
          <p class="body">{c.body}</p>
        </li>
      {/each}
    </ul>
  {/if}

  {#if canComment}
    <form onsubmit={submit} class="stack" aria-label="Add a comment">
      <label for="comment-text">Add a comment</label>
      <textarea id="comment-text" rows="3" bind:value={text} disabled={busy} aria-describedby="comment-count"></textarea>
      <div class="row">
        <button type="submit" class="btn primary" disabled={busy || commentLength(text) === 0}>Comment</button>
        <span id="comment-count" class="muted" class:over={commentLength(text) > COMMENT_MAX}>
          {commentLength(text)} / {COMMENT_MAX}
        </span>
      </div>
      {#if formError}<p class="error" role="alert">{formError}</p>{/if}
    </form>
  {:else}
    <p class="muted">Viewers can read comments but not write them.</p>
  {/if}
</div>

<style>
  .thread { list-style: none; padding: 0; margin: 0; display: flex; flex-direction: column; gap: 12px; }
  .thread li { background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius); padding: 8px 12px; }
  .meta { display: flex; gap: 8px; align-items: baseline; }
  .body { margin: 4px 0 0; white-space: pre-wrap; overflow-wrap: anywhere; }
  textarea { width: 100%; font: inherit; resize: vertical; }
  .over { color: var(--danger, #b42318); }
</style>

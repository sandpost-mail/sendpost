import { api, errorMessage } from './api';
import type { MessageSummary } from './types';

const PAGE_SIZE = 50;
const FRESH_MS = 4000;

/** Reactive message list of one project: search, infinite scroll and live merge. */
export class Inbox {
  items = $state<MessageSummary[]>([]);
  query = $state('');
  loading = $state(false);
  done = $state(false);
  error = $state('');
  /** Ids that just arrived live, used to flash them briefly. */
  fresh = $state<number[]>([]);
  /** Bumped on every live event that may change a comment thread, so an open thread reloads. */
  liveVersion = $state(0);

  private cursor = '';
  private generation = 0;

  constructor(readonly projectId: number) {}

  /** Replace the list with the first page for `query`. */
  async reload(query = this.query): Promise<void> {
    const gen = ++this.generation;
    this.query = query;
    this.loading = true;
    this.error = '';
    try {
      const page = await api.listMessages(this.projectId, query, '', PAGE_SIZE);
      if (gen !== this.generation) return;
      this.items = page.items;
      this.cursor = page.next_cursor;
      this.done = page.next_cursor === '';
    } catch (e) {
      if (gen === this.generation) this.error = errorMessage(e);
    } finally {
      if (gen === this.generation) this.loading = false;
    }
  }

  /** Append the next page (infinite scroll). */
  async loadMore(): Promise<void> {
    if (this.loading || this.done || this.cursor === '') return;
    const gen = this.generation;
    this.loading = true;
    try {
      const page = await api.listMessages(this.projectId, this.query, this.cursor, PAGE_SIZE);
      if (gen !== this.generation) return;
      const seen = new Set(this.items.map((m) => m.id));
      this.items = [...this.items, ...page.items.filter((m) => !seen.has(m.id))];
      this.cursor = page.next_cursor;
      this.done = page.next_cursor === '';
    } catch (e) {
      if (gen === this.generation) this.error = errorMessage(e);
    } finally {
      if (gen === this.generation) this.loading = false;
    }
  }

  /**
   * Merge the newest page into the list without losing the scroll position or
   * the pages already loaded. Used for live updates and after reconnects.
   */
  async refreshHead(): Promise<void> {
    const gen = this.generation;
    try {
      const page = await api.listMessages(this.projectId, this.query, '', PAGE_SIZE);
      if (gen !== this.generation) return;
      const known = new Map(this.items.map((m) => [m.id, m]));
      const arrived = page.items.filter((m) => !known.has(m.id));
      if (arrived.length === 0) return;
      const headIds = new Set(page.items.map((m) => m.id));
      // Newest page first, then older rows that are not part of it.
      this.items = [...page.items, ...this.items.filter((m) => !headIds.has(m.id))];
      this.flash(arrived.map((m) => m.id));
    } catch {
      // A failed background refresh is not worth an error banner.
    }
  }

  /** Tell open comment threads that something may have changed (comment.created, reconnect). */
  touch(): void {
    this.liveVersion++;
  }

  markRead(id: number): void {
    const at = new Date().toISOString();
    this.items = this.items.map((m) => (m.id === id && !m.read_at ? { ...m, read_at: at } : m));
  }

  private flash(ids: number[]): void {
    this.fresh = [...this.fresh, ...ids];
    setTimeout(() => {
      this.fresh = this.fresh.filter((id) => !ids.includes(id));
    }, FRESH_MS);
  }
}

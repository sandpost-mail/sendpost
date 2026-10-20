<script lang="ts">
  import { onMount } from 'svelte';
  import { errorMessage } from '$lib/api';
  import { fullDate, relativeTime } from '$lib/format';
  import type { IssuedToken, Token, TokenInput, TokenScope } from '$lib/types';
  import * as v from '$lib/validate';
  import SecretNotice from './SecretNotice.svelte';
  import TextField from './TextField.svelte';

  interface Props {
    /** Unique on the page, prefixes the element ids. */
    id: string;
    heading: string;
    intro: string;
    /** Access levels offered when creating a token. */
    scopes: TokenScope[];
    load: () => Promise<Token[]>;
    /** Omit for a list that can only be revoked (an administrator looking at another user). */
    create?: (input: TokenInput) => Promise<IssuedToken>;
    revoke: (token: Token) => Promise<void>;
  }

  let { id, heading, intro, scopes, load, create, revoke }: Props = $props();

  const SCOPE_TEXT: Record<TokenScope, string> = {
    read: 'Read only (viewer level)',
    write: 'Read and write (member level)',
    manage: 'Full access of the owner'
  };

  let tokens = $state<Token[]>([]);
  let loaded = $state(false);
  let label = $state('');
  let labelError = $state('');
  // svelte-ignore state_referenced_locally
  let level = $state<TokenScope>(scopes[0]);
  let issued = $state<IssuedToken | null>(null);
  let busy = $state(false);
  let error = $state('');

  onMount(async () => {
    try {
      tokens = await load();
    } catch (e) {
      error = errorMessage(e);
    } finally {
      loaded = true;
    }
  });

  async function submit(event: SubmitEvent) {
    event.preventDefault();
    if (!create) return;
    labelError = v.optionalMax(label, 'Label', 100);
    if (labelError) return;
    busy = true;
    error = '';
    try {
      const created = await create({ label: label.trim(), scopes: [level] });
      const { secret: _secret, ...stored } = created;
      tokens = [stored, ...tokens];
      issued = created;
      label = '';
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = false;
    }
  }

  async function remove(token: Token) {
    if (!confirm(`Revoke ${token.prefix}…? Anything using it will stop working.`)) return;
    error = '';
    try {
      await revoke(token);
      tokens = tokens.map((t) => (t.id === token.id ? { ...t, revoked_at: new Date().toISOString() } : t));
      if (issued?.id === token.id) issued = null;
    } catch (e) {
      error = errorMessage(e);
    }
  }
</script>

<section class="card stack" aria-labelledby="{id}-heading">
  <h2 id="{id}-heading">{heading}</h2>
  <p class="muted">{intro}</p>

  {#if create}
    <form class="stack" onsubmit={submit} novalidate>
      <TextField
        id="{id}-label"
        label="Label"
        placeholder="for example CI pipeline"
        maxlength={100}
        bind:value={label}
        error={labelError}
      />
      <fieldset class="levels">
        <legend>Access level</legend>
        {#each scopes as s (s)}
          <label class="radio">
            <input type="radio" name="{id}-level" value={s} bind:group={level} />
            {SCOPE_TEXT[s]}
          </label>
        {/each}
      </fieldset>
      <div><button class="btn primary" disabled={busy}>Create token</button></div>
    </form>
  {/if}
  {#if error}<p class="error" role="alert">{error}</p>{/if}

  {#if issued}
    <SecretNotice secret={issued.secret} label="token" ondismiss={() => (issued = null)} />
  {/if}

  {#if !loaded}
    <p class="muted">Loading…</p>
  {:else if tokens.length === 0}
    <p class="muted">No tokens yet.</p>
  {:else}
    <table>
      <thead><tr><th>Token</th><th>Label</th><th>Access</th><th>Created</th><th>Last used</th><th></th></tr></thead>
      <tbody>
        {#each tokens as t (t.id)}
          <tr class:revoked={t.revoked_at}>
            <td class="mono">{t.prefix}…</td>
            <td>{t.label || '-'}</td>
            <td>{t.scopes.join(', ')}</td>
            <td title={fullDate(t.created_at)}>{relativeTime(t.created_at)}</td>
            <td>{t.last_used_at ? relativeTime(t.last_used_at) : 'never'}</td>
            <td class="actions">
              {#if t.revoked_at}
                <span class="badge danger">revoked</span>
              {:else}
                <button type="button" class="btn small danger" onclick={() => remove(t)}>Revoke</button>
              {/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
</section>

<style>
  .levels { border: 0; padding: 0; margin: 0; display: flex; flex-direction: column; gap: 4px; }
  .levels legend { font-weight: 600; font-size: 0.9em; padding: 0; margin-bottom: 4px; }
  .radio { display: flex; gap: 8px; align-items: center; }
  tr.revoked td { color: var(--muted); text-decoration: line-through; }
  tr.revoked td:last-child { text-decoration: none; }
  .actions { text-align: right; }
</style>

<script lang="ts">
  import type { HTMLInputAttributes } from 'svelte/elements';

  interface Props extends Omit<HTMLInputAttributes, 'value' | 'id'> {
    id: string;
    label: string;
    value: string;
    /** Validation message, shown under the field and announced to assistive tech. */
    error?: string;
    hint?: string;
  }

  let { id, label, value = $bindable(), error = '', hint = '', ...rest }: Props = $props();

  let describedBy = $derived([hint && `${id}-hint`, error && `${id}-error`].filter(Boolean).join(' '));
</script>

<div class="field">
  <label for={id}>{label}</label>
  <input
    {id}
    class="input"
    bind:value
    aria-invalid={error ? 'true' : undefined}
    aria-describedby={describedBy || undefined}
    {...rest}
  />
  {#if hint}<span id="{id}-hint" class="muted hint">{hint}</span>{/if}
  {#if error}<span id="{id}-error" class="field-error" role="alert">{error}</span>{/if}
</div>

<style>
  .hint { font-size: 0.85em; }
  .field-error { color: var(--danger); font-size: 0.85em; }
</style>

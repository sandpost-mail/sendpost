<script lang="ts">
  import CopyButton from './CopyButton.svelte';

  let { secret, label, ondismiss }: { secret: string; label: string; ondismiss: () => void } = $props();
</script>

<div class="notice stack" role="status">
  <strong>Copy the {label} now. It will not be shown again.</strong>
  <code class="secret mono" data-testid="issued-secret">{secret}</code>
  <div class="row">
    <CopyButton text={secret} label="Copy {label}" />
    <button type="button" class="btn small" onclick={ondismiss}>I have copied it</button>
  </div>
</div>

<style>
  .notice { border: 1px solid var(--accent); background: var(--accent-soft); border-radius: var(--radius); padding: 12px; }
  .secret { display: block; padding: 8px 10px; background: var(--surface); border-radius: var(--radius); overflow-wrap: anywhere; user-select: all; }
</style>

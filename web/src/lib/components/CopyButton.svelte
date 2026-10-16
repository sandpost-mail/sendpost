<script lang="ts">
  import { copyText } from '$lib/clipboard';

  let { text, label = 'Copy' }: { text: string; label?: string } = $props();
  let state = $state<'idle' | 'copied' | 'failed'>('idle');

  async function copy() {
    state = (await copyText(text)) ? 'copied' : 'failed';
    setTimeout(() => (state = 'idle'), 1800);
  }
</script>

<button type="button" class="btn small" onclick={copy} aria-live="polite">
  {state === 'copied' ? 'Copied' : state === 'failed' ? 'Copy failed' : label}
</button>

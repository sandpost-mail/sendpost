<script lang="ts">
  import CopyButton from './CopyButton.svelte';
  import { envSnippet } from '$lib/envSnippet';
  import type { ServerInfo } from '$lib/types';

  let {
    info,
    username,
    secret
  }: { info: ServerInfo | null; username: string; secret?: string } = $props();

  let text = $derived(envSnippet({ host: window.location.hostname, info, username, secret }));
</script>

<div class="snippet">
  <pre class="block mono">{text}</pre>
  <CopyButton {text} label="Copy snippet" />
</div>

<style>
  .snippet { display: flex; flex-direction: column; gap: 8px; align-items: flex-start; }
  .snippet pre { align-self: stretch; }
</style>

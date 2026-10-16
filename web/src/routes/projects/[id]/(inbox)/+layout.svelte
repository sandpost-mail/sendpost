<script lang="ts">
  import InboxShell from '$lib/components/InboxShell.svelte';

  let { data, children } = $props();
</script>

<!-- Keyed so switching project rebuilds the list, the stream and the search. -->
{#key data.projectId}
  <InboxShell projectId={data.projectId}>
    {@render children()}
  </InboxShell>
{/key}

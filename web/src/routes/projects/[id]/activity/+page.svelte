<script lang="ts">
  import ActivityFeed from '$lib/components/ActivityFeed.svelte';

  let { data } = $props();
</script>

<svelte:head><title>Activity · {data.project.name} · Sandpost</title></svelte:head>

<div class="page stack">
  <header>
    <h1>Activity</h1>
    <p class="muted">Messages received, comments and releases in {data.project.name}, newest first.</p>
  </header>
  {#key data.projectId}
    <ActivityFeed projectId={data.projectId} />
  {/key}
</div>

<style>
  .page { max-width: 1000px; margin: 24px auto; padding: 0 16px; }
</style>

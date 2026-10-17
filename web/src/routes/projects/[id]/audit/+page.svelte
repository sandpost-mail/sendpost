<script lang="ts">
  import { api } from '$lib/api';
  import AuditTable from '$lib/components/AuditTable.svelte';

  let { data } = $props();
</script>

<svelte:head><title>Audit · {data.project.name} · Sandpost</title></svelte:head>

<div class="page stack">
  <header>
    <h1>Audit log</h1>
    <p class="muted">Changes made to {data.project.name}, newest first.</p>
  </header>
  {#key data.projectId}
    <AuditTable load={(cursor) => api.listProjectAudit(data.projectId, cursor)} />
  {/key}
</div>

<style>
  .page { max-width: 1000px; margin: 24px auto; padding: 0 16px; }
</style>

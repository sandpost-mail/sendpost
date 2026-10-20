<script lang="ts">
  import { api } from '$lib/api';
  import AuditTable from '$lib/components/AuditTable.svelte';
</script>

<svelte:head><title>Audit log · Sandpost</title></svelte:head>

<div class="stack">
  <header>
    <h1>Audit log</h1>
    <p class="muted">Administrative actions across the instance, newest first.</p>
  </header>
  <AuditTable load={(cursor) => api.listAudit(cursor)} showProject />
</div>

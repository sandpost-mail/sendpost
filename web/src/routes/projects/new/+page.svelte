<script lang="ts">
  import NewProjectForm from '$lib/components/NewProjectForm.svelte';
</script>

<div class="page">
  <h1>New project</h1>
  <NewProjectForm />
</div>

<style>
  .page { max-width: 520px; margin: 48px auto; padding: 0 16px; display: flex; flex-direction: column; gap: 16px; }
</style>

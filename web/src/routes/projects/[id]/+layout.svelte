<script lang="ts">
  import { rememberProject } from '$lib/projects.svelte';

  let { data, children } = $props();

  $effect(() => rememberProject(data.projectId));
</script>

{@render children()}

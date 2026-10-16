<script lang="ts">
  import { page } from '$app/state';
</script>

<div class="wrap">
  <h1>{page.status}</h1>
  <p class="muted">{page.error?.message ?? 'Something went wrong'}</p>
  <a class="btn" href="/">Back to Sandpost</a>
</div>

<style>
  .wrap { max-width: 520px; margin: 64px auto; padding: 0 16px; display: flex; flex-direction: column; gap: 12px; align-items: flex-start; }
</style>

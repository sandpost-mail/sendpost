<script lang="ts">
  import { page } from '$app/state';

  let { children } = $props();

  const SECTIONS = [
    { href: '/admin/users', label: 'Users' },
    { href: '/admin/groups', label: 'Groups' },
    { href: '/admin/audit', label: 'Audit log' }
  ];
</script>

<div class="admin">
  <nav aria-label="Administration sections">
    {#each SECTIONS as s (s.href)}
      <a href={s.href} aria-current={page.url.pathname.startsWith(s.href) ? 'page' : undefined}>{s.label}</a>
    {/each}
  </nav>
  <div class="content">
    {@render children()}
  </div>
</div>

<style>
  .admin { max-width: 1000px; margin: 24px auto; padding: 0 16px; display: flex; flex-direction: column; gap: 16px; }
  nav { display: flex; gap: 4px; border-bottom: 1px solid var(--border); padding-bottom: 8px; }
  nav a { padding: 4px 10px; border-radius: var(--radius); text-decoration: none; color: var(--muted); }
  nav a[aria-current='page'] { background: var(--accent-soft); color: var(--accent); }
</style>

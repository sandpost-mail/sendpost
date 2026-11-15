import adapter from '@sveltejs/adapter-static';
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

/** @type {import('@sveltejs/kit').Config} */
const config = {
  preprocess: vitePreprocess(),
  kit: {
    // SPA mode: every route falls back to index.html, embedded later by go:embed.
    adapter: adapter({ fallback: 'index.html' }),
    // The inline bootstrap script is allowed by its hash, not 'unsafe-inline'
    // (which would also let javascript: URLs run). SvelteKit writes the hash
    // into a <meta> CSP; internal/webui computes the same hash for the header.
    csp: {
      mode: 'hash',
      directives: {
        'script-src': ['self']
      }
    }
  }
};

export default config;

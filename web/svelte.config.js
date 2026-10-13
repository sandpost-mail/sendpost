import adapter from '@sveltejs/adapter-static';
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

/** @type {import('@sveltejs/kit').Config} */
const config = {
  preprocess: vitePreprocess(),
  kit: {
    // SPA mode: every route falls back to index.html, embedded later by go:embed.
    adapter: adapter({ fallback: 'index.html' })
  }
};

export default config;

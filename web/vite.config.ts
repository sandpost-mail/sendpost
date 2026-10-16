import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

export default defineConfig({
  plugins: [sveltekit()],
  server: {
    // `make dev` serves the API on :8080, so the hot reload server can use it.
    proxy: { '/_/api': 'http://127.0.0.1:8080' }
  }
});

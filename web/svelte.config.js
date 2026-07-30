import adapter from '@sveltejs/adapter-static';

export default {
  kit: {
    // Static output: Caddy serves it directly, no Node runtime on the VPS.
    adapter: adapter({ fallback: 'index.html', precompress: true }),
    prerender: { entries: [] },
    paths: { base: '' }
  }
};

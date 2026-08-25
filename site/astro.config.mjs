import { defineConfig } from 'astro/config';

export default defineConfig({
  site: 'https://tma1.ai',
  output: 'static',
  // TMA1 stays at the locale root so every existing URL keeps working. /tma1
  // is the shorthand people reach for now that the site hosts three projects;
  // it redirects instead of serving the same page under a second URL.
  redirects: {
    '/tma1': '/',
    '/zh/tma1': '/zh/',
    '/es/tma1': '/es/',
  },
});

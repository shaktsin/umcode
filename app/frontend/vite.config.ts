import { defineConfig, type Plugin } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import tailwindcss from '@tailwindcss/vite';
import { readFileSync } from 'node:fs';
import { homedir } from 'node:os';
import { join } from 'node:path';

// In the Mac app the Go shell answers /__umcode/connection. During `npm run dev`
// this plugin does the same from the local engine's token file, so the UI can be
// developed in a normal browser against a running `umcode engine`.
function engineConnection(): Plugin {
  return {
    name: 'umcode-engine-connection',
    configureServer(server) {
      server.middlewares.use('/__umcode/connection', (_req, res) => {
        const home = process.env.UMCODE_HOME || join(homedir(), '.umcode');
        const port = process.env.UMCODE_ENGINE_WS_PORT || '8766';
        res.setHeader('Content-Type', 'application/json');
        res.setHeader('Cache-Control', 'no-store');
        try {
          const token = readFileSync(join(home, 'run', 'token'), 'utf8').trim();
          res.end(JSON.stringify({ url: `ws://127.0.0.1:${port}/ws`, token, shell: 'browser' }));
        } catch (err) {
          res.statusCode = 503;
          res.end(JSON.stringify({ error: `engine token not found in ${home}/run/token — is \`umcode engine\` running?` }));
        }
      });
    },
  };
}

export default defineConfig({
  plugins: [tailwindcss(), svelte(), engineConnection()],
  resolve: { alias: { $lib: '/src/lib' } },
  server: { port: 5173, strictPort: true, host: '127.0.0.1' },
  build: { outDir: 'dist', emptyOutDir: true, target: 'safari16' },
});

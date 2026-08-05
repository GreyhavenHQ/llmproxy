import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import path from 'node:path'

// Build output goes straight into the Go module and is embedded into the
// binary via go:embed (internal/server/ui.go). Commit the dist so `go build`
// works without a node toolchain.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { '@': path.resolve(__dirname, 'src') },
  },
  build: {
    outDir: '../internal/server/uidist',
    emptyOutDir: true,
  },
  server: {
    // `npm run dev` against a locally running proxy.
    proxy: {
      '/auth': 'http://127.0.0.1:4000',
      '/my': 'http://127.0.0.1:4000',
      '/admin': 'http://127.0.0.1:4000',
      '/v1': 'http://127.0.0.1:4000',
    },
  },
})

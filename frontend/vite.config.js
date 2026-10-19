import { fileURLToPath, URL } from 'node:url'
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// HUB_API lets a dev daemon on a non-default port drive the same UI.
const api = process.env.HUB_API || 'http://localhost:9000'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    proxy: {
      '/api': api,
      '/ws': { target: api.replace(/^http/, 'ws'), ws: true },
    },
  },
})

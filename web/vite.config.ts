import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// In development the API runs separately on :8080; the built app is
// embedded in the trackside binary and served from the same origin.
export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      '/v1': 'http://localhost:8080',
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    chunkSizeWarningLimit: 600,
  },
})

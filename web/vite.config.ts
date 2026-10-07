import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// In development the API runs separately on :8080; the built app is
// embedded in the trackside binary and served from the same origin.
export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      // ws: the live updates socket at /v1/live goes through the same proxy.
      // TRACKSIDE_API points development at another server.
      '/v1': { target: process.env.TRACKSIDE_API ?? 'http://localhost:8080', ws: true },
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    chunkSizeWarningLimit: 600,
  },
})

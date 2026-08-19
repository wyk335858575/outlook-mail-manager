import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  build: {
    emptyOutDir: false,
    outDir: 'dist',
  },
  server: {
    host: '127.0.0.1',
    port: 5173,
    proxy: {
      '/healthz': 'http://127.0.0.1:8080',
    },
  },
})

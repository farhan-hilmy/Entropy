import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// `npm run dev` serves the UI on :5173 and forwards /api to a running `entropy up`.
export default defineConfig({
  plugins: [react()],
  server: {
    proxy: { '/api': 'http://127.0.0.1:7400' },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
})

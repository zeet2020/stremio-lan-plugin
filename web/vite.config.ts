import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// `npm run dev` talks to a stremio-lan running on :7070
const api = 'http://localhost:7070'

export default defineConfig({
  plugins: [react()],
  server: { proxy: { '/api': api, '/rescan': api } },
})

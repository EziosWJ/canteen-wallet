import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      '/api': { target: process.env.CANTEEN_WEB_API_TARGET || 'http://127.0.0.1:8080', changeOrigin: true },
      '/healthz': { target: process.env.CANTEEN_WEB_API_TARGET || 'http://127.0.0.1:8080', changeOrigin: true },
    },
  },
});

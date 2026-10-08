import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// `npm run dev` proxies API and video to a locally running sentinel server.
const target = process.env.SENTINEL_URL ?? "http://127.0.0.1:8091";

export default defineConfig({
  plugins: [react()],
  build: { outDir: "dist", emptyOutDir: true, sourcemap: false, chunkSizeWarningLimit: 1200 },
  server: {
    proxy: {
      "/api": target,
      "/live": target,
      "/recordings": target,
    },
  },
});

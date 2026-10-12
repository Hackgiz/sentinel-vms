import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// `npm run dev` proxies API and video to a locally running sentinel server.
const target = process.env.SENTINEL_URL ?? "http://127.0.0.1:8091";

// Two pages: the dashboard and Sentinel Admin (index.html, embedded in the
// server) and Sentinel Operator (operator.html, also embedded; the desktop
// app in linux/desktop bundles its own copy from `npm run build:operator`,
// which builds only that page into dist-operator/).
export default defineConfig(({ mode }) => ({
  plugins: [react()],
  build:
    mode === "operator"
      ? { outDir: "dist-operator", emptyOutDir: true, sourcemap: false, chunkSizeWarningLimit: 1200, rollupOptions: { input: { operator: "operator.html" } } }
      : { outDir: "dist", emptyOutDir: true, sourcemap: false, chunkSizeWarningLimit: 1200, rollupOptions: { input: { index: "index.html", operator: "operator.html" } } },
  server: {
    proxy: {
      "/api": target,
      "/live": target,
      "/recordings": target,
    },
  },
}));

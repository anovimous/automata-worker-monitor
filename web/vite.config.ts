/// <reference types="vitest" />
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import path from "node:path";

// In development the API is the workers control plane (VITE_DEV_API_TARGET, default 127.0.0.1:9490). Its Origin check
// refuses a request whose Origin is another site, so the proxy drops the header: the browser's is the dev server's.
const target = process.env.VITE_DEV_API_TARGET || "http://127.0.0.1:9490";
const proxy = {
  target,
  configure: (p: { on: (event: "proxyReq", fn: (req: { removeHeader: (name: string) => void }) => void) => void }) =>
    p.on("proxyReq", (req) => req.removeHeader("origin")),
};

export default defineConfig({
  plugins: [react()],
  resolve: { alias: { "@": path.resolve(__dirname, "./src") } },
  server: { port: 5173, proxy: { "/api": proxy, "/healthz": proxy } },
  // one bundle, served from the binary on the local network
  build: { target: "es2022", sourcemap: false, chunkSizeWarningLimit: 800 },
  test: { environment: "jsdom", setupFiles: ["./src/test/setup.ts"], globals: true },
});

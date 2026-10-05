import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: {
    // The Go server embeds this directory (internal/web).
    outDir: "../internal/web/dist",
    emptyOutDir: true,
  },
  server: {
    // During `npm run dev`, forward API calls to a running acs-server.
    proxy: { "/api": "http://localhost:8080" },
  },
});

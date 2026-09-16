import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";

// The build is committed as ../dist and embedded into the projmux binary, so it
// has to be reproducible: the same source must give the same bytes, or
// `make web-check` would fail on every machine but the one that built it.
// Asset names carry a content hash, which is deterministic, and nothing that
// varies per build (time, absolute paths) is written into the output.
export default defineConfig({
  plugins: [svelte()],
  base: "/",
  build: {
    outDir: "../dist",
    emptyOutDir: true,
    assetsDir: "assets",
    sourcemap: false,
    target: "es2022",
  },
  server: {
    proxy: {
      "/api": "http://127.0.0.1:8787",
    },
  },
});

import { defineConfig } from "tsup";

export default defineConfig([
  {
    entry: ["src/index.ts", "src/proto/index.ts", "src/cli.ts"],
    format: ["esm"],
    outDir: "dist/esm",
    dts: true,
    sourcemap: true,
    clean: true,
    target: "es2022",
    splitting: false,
  },
  {
    entry: ["src/index.ts", "src/proto/index.ts"],
    format: ["cjs"],
    outDir: "dist/cjs",
    // engine-downloader reads import.meta.url only when __dirname is undefined, never in CJS.
    esbuildOptions(options) {
      options.logOverride = { ...options.logOverride, "empty-import-meta": "silent" };
    },
    dts: true,
    sourcemap: true,
    clean: false,
    target: "es2022",
    splitting: false,
  },
]);

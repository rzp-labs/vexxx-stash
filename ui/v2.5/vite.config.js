import { defineConfig, loadEnv } from "vite";
import react from "@vitejs/plugin-react";
import legacy from "@vitejs/plugin-legacy";
import tsconfigPaths from "vite-tsconfig-paths";
import viteCompression from "vite-plugin-compression";
import posthog from "@posthog/rollup-plugin";
import { uploadsEnabled } from "./scripts/posthog-build-config.mjs";
import path from "path";
import { fileURLToPath } from "url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));

const nolegacy = process.env.VITE_APP_NOLEGACY === "true";
const sourcemap = process.env.VITE_APP_SOURCEMAPS === "true";

// https://vitejs.dev/config/
export default defineConfig(({ command, mode }) => {
  const env = loadEnv(mode, __dirname, "");
  const uploadSourceMaps = uploadsEnabled(command, env);

  if (
    command === "build" &&
    env.POSTHOG_UPLOAD_REQUIRED === "true" &&
    !uploadSourceMaps
  ) {
    throw new Error(
      "PostHog source map upload requires POSTHOG_API_KEY and POSTHOG_PROJECT_ID"
    );
  }

  let plugins = [
    react({
      babel: {
        compact: true,
      },
    }),
    tsconfigPaths(),
    viteCompression({
      algorithm: "gzip",
      deleteOriginFile: true,
      threshold: 0,
      filter: /\.(js|json|css|svg|md)$/i,
    }),
  ];

  if (!nolegacy) {
    plugins = [...plugins, legacy()];
  }

  if (uploadSourceMaps) {
    plugins.push(
      posthog({
        personalApiKey: env.POSTHOG_API_KEY,
        projectId: env.POSTHOG_PROJECT_ID,
        host: env.POSTHOG_HOST,
        sourcemaps: {
          enabled: true,
          releaseName: "vexxx-ui",
          releaseVersion:
            env.VITE_APP_GITHASH || env.GITHUB_SHA || "development",
          deleteAfterUpload: true,
        },
      }),
      {
        name: "posthog-omit-polyfill-maps",
        generateBundle: {
          order: "post",
          handler(_options, bundle) {
            // Vite builds these separately, without PostHog chunk IDs.
            for (const chunk of Object.values(bundle)) {
              if (
                chunk.type === "chunk" &&
                chunk.facadeModuleId === "\0vite/legacy-polyfills" &&
                chunk.sourcemapFileName
              ) {
                delete bundle[chunk.sourcemapFileName];
              }
            }
          },
        },
      }
    );
  }

  return {
    base: "",
    resolve: {
      alias: {
        src: path.resolve(__dirname, "src"),
      },
    },
    build: {
      outDir: "build",
      sourcemap: uploadSourceMaps ? "hidden" : sourcemap,
      reportCompressedSize: false,
      // Bound aggregate memory, including the legacy minifier worker.
      terserOptions: { maxWorkers: 1 },
    },
    optimizeDeps: {
      entries: "src/index.tsx",
    },
    server: {
      port: 3000,
      cors: false,
      proxy: {
        "/scheduled-tasks": {
          target: "http://localhost:9999",
          changeOrigin: true,
          secure: false,
        },
        // local Handy Bluetooth bridge (WebSocket) — must proxy with ws:true
        // or the dev server 404s the upgrade request
        "/handy": {
          target: "http://localhost:9999",
          changeOrigin: true,
          secure: false,
          ws: true,
        },
        "/graphql": {
          target: "http://localhost:9999",
          changeOrigin: true,
          secure: false,
        },
        "/stashface": {
          target: "http://localhost:9999",
          changeOrigin: true,
          secure: false,
        },
        "/stashtag": {
          target: "http://localhost:9999",
          changeOrigin: true,
          secure: false,
        },
      },
    },
    css: {
      preprocessorOptions: {
        scss: {
          api: "modern-compiler",
        },
      },
    },
    publicDir: "public",
    assetsInclude: ["**/*.md"],
    plugins,
  };
});

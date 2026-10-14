import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import tsconfigPaths from "vite-tsconfig-paths";
import { fileURLToPath } from "node:url";

export default defineConfig({
  plugins: [react(), tsconfigPaths()],
  test: {
    globals: true,
    // Bundle MUI's ESM imports so React 17 jsx-runtime resolves consistently.
    server: { deps: { inline: [/@mui/] } },
    environment: "jsdom",
    setupFiles: ["./src/test/setup.ts"],
    include: ["src/**/*.{test,spec}.{ts,tsx}"],
    coverage: {
      provider: "v8",
      reporter: [
        "text",
        "json",
        "html",
        [
          "lcov",
          { projectRoot: fileURLToPath(new URL("../..", import.meta.url)) },
        ],
      ],
      reportsDirectory: "../../dist/coverage/frontend",
      include: ["src/**/*.{ts,tsx}"],
      exclude: [
        "src/test/**",
        "src/**/*.d.ts",
        "src/**/*.{test,spec}.{ts,tsx}",
        "src/core/generated-graphql.ts",
      ],
    },
    css: true,
  },
});

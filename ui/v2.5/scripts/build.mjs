import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { loadEnv } from "vite";
import { buildHeapMB } from "./posthog-build-config.mjs";

const args = process.argv.slice(2);
const modeOption = args.findIndex(
  (arg) => arg === "--mode" || arg === "-m" || arg.startsWith("--mode=")
);
const mode =
  modeOption < 0
    ? "production"
    : args[modeOption].startsWith("--mode=")
    ? args[modeOption].slice(7)
    : args[modeOption + 1];
const root = fileURLToPath(new URL("../", import.meta.url));
const env = loadEnv(mode, root, "");
const vite = fileURLToPath(
  new URL("../node_modules/vite/bin/vite.js", import.meta.url)
);
const result = spawnSync(
  process.execPath,
  [`--max-old-space-size=${buildHeapMB(env)}`, vite, "build", ...args],
  { cwd: root, stdio: "inherit", env: process.env }
);
if (result.error) throw result.error;
process.exit(result.status ?? 1);

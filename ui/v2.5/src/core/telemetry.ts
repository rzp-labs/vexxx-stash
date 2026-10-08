import posthog, { CaptureResult, PostHogConfig } from "posthog-js/no-external";
// Bundle the error parser locally; never load remote code into the private UI.
import "posthog-js/dist/exception-autocapture";
import { diagnosticContext, diagnosticMessage } from "./telemetry-redaction";

const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const identity = /^(\d+|[0-9a-f-]{36})$/i;
// The embedded UI uses Vite's hashed bundles under its own assets directory.
// Plugin, media and foreign scripts must never become diagnostic filenames.
function bundleFilename(value: unknown): string | undefined {
  if (typeof value !== "string") return undefined;
  try {
    const base = new URL(
      document.querySelector("base")?.href || "/",
      window.location.origin
    );
    const assets = new URL("assets/", base);
    const frame = new URL(value, base);
    if (
      frame.origin !== window.location.origin ||
      assets.origin !== window.location.origin ||
      !frame.pathname.startsWith(assets.pathname)
    )
      return undefined;
    const name = frame.pathname.slice(assets.pathname.length);
    return /^[A-Za-z0-9_.-]+-[A-Za-z0-9_-]{8}\.js$/.test(name)
      ? `/assets/${name}`
      : undefined;
  } catch {
    return undefined;
  }
}
function diagnosticScreen(): string {
  return (
    window.location.pathname
      .split("/")
      .find((part) =>
        [
          "scenes",
          "images",
          "galleries",
          "performers",
          "studios",
          "tags",
          "settings",
          "stats",
          "playlists",
        ].includes(part)
      ) || "other"
  );
}

// Rebuild the SDK envelope, then retain explicitly supplied diagnostic context.
// The SDK adds
// URLs, referrers and persisted campaign/person properties even to manual errors.
export function sanitizeTelemetry(
  event: CaptureResult | null
): CaptureResult | null {
  if (!event || !["$pageview", "$identify", "$exception"].includes(event.event))
    return null;
  const source = event.properties;
  const properties: CaptureResult["properties"] = {
    token: source.token, // The SDK's public ingestion token, never an upload key.
    $geoip_disable: true,
    $lib: "web",
    $lib_version: source.$lib_version,
    $process_person_profile: false,
    // These are PostHog's application/release fields. Always derive them from
    // the embedded build, rather than accepting arbitrary incoming properties.
    $app_namespace: "vexxx-ui",
    $app_version: import.meta.env.VITE_APP_STASH_VERSION || "development",
    $app_build: import.meta.env.VITE_APP_GITHASH || "development",
  };
  for (const key of ["distinct_id", "$user_id", "$anon_distinct_id"]) {
    if (typeof source[key] === "string" && identity.test(source[key]))
      properties[key] = source[key];
  }
  for (const key of ["$session_id", "$window_id"]) {
    if (typeof source[key] === "string" && uuid.test(source[key]))
      properties[key] = source[key];
  }
  if (event.event === "$identify") {
    const role = event.$set?.user_role ?? source.$set?.user_role;
    if (
      !["admin", "viewer", "ADMIN", "VIEWER"].includes(role) ||
      typeof source.distinct_id !== "string" ||
      !/^[1-9]\d*$/.test(source.distinct_id) ||
      source.$user_id !== source.distinct_id
    )
      return null;
    properties.$set = { user_role: role };
    properties.$process_person_profile = true;
  }
  if (event.event === "$pageview") {
    // Only fixed screen categories, never item IDs, titles, queries or proxy paths.
    properties.screen = diagnosticScreen();
  }
  if (event.event === "$exception") {
    properties.screen = diagnosticScreen();
    properties.app_revision = import.meta.env.VITE_APP_GITHASH || "development";
    if (
      ["fatal", "error", "warning", "log", "info", "debug"].includes(
        source.$exception_level
      )
    )
      properties.$exception_level = source.$exception_level;
    for (const key of [
      "operation",
      "stage",
      "component",
      "code",
      "status",
      "retry_count",
      "context",
      "diagnostic_context",
    ]) {
      const safe = diagnosticContext(source[key]);
      if (safe !== undefined) properties[key] = safe;
    }
    properties.$exception_list = (
      Array.isArray(source.$exception_list) ? source.$exception_list : []
    )
      .slice(0, 10)
      .filter((item) => item && typeof item === "object")
      .map((item) => ({
        type:
          typeof item.type === "string" &&
          /^[A-Za-z_$][\w.$]{0,79}$/.test(item.type)
            ? diagnosticMessage(item.type)
            : "Error",
        value: diagnosticMessage(item.value),
        mechanism: {
          ...(typeof item.mechanism?.handled === "boolean"
            ? { handled: item.mechanism.handled }
            : {}),
          ...(typeof item.mechanism?.synthetic === "boolean"
            ? { synthetic: item.mechanism.synthetic }
            : {}),
          ...([
            "generic",
            "chained",
            "onerror",
            "onunhandledrejection",
          ].includes(item.mechanism?.type)
            ? { type: item.mechanism.type }
            : {}),
          ...(["cause", "member"].includes(item.mechanism?.source)
            ? { source: item.mechanism.source }
            : {}),
          ...Object.fromEntries(
            ["exception_id", "parent_id"].flatMap((key) =>
              Number.isSafeInteger(item.mechanism?.[key]) &&
              item.mechanism[key] >= 0
                ? [[key, item.mechanism[key]]]
                : []
            )
          ),
        },
        stacktrace: {
          type: "raw",
          frames: (Array.isArray(item.stacktrace?.frames)
            ? item.stacktrace.frames
            : []
          )
            .slice(0, 50)
            .flatMap((frame: Record<string, unknown>) => {
              if (!frame || typeof frame !== "object") return [];
              const filename = bundleFilename(frame.filename);
              if (!filename) return [];
              const safe: Record<string, unknown> = {
                filename,
                platform: "web:javascript",
                in_app: true,
                // Ingestion requires a function string. Use the pinned parser's
                // safe sentinel when an anonymous/unsafe name is redacted.
                function: "?",
              };
              for (const key of ["lineno", "colno"]) {
                if (Number.isSafeInteger(frame[key]) && Number(frame[key]) >= 0)
                  safe[key] = frame[key];
              }
              if (
                typeof frame.function === "string" &&
                /^[A-Za-z_$][\w.$<> ]{0,150}$/.test(frame.function)
              )
                safe.function = frame.function;
              if (
                typeof frame.chunk_id === "string" &&
                uuid.test(frame.chunk_id)
              )
                safe.chunk_id = frame.chunk_id;
              return [safe];
            }),
        },
      }));
  }
  // Omit top-level $set/$set_once and every arbitrary payload/attachment too.
  return {
    uuid: event.uuid,
    event: event.event,
    timestamp: event.timestamp,
    properties,
  };
}

export const telemetryConfig: Partial<PostHogConfig> = {
  defaults: "2026-01-30",
  autocapture: false,
  capture_pageview: "history_change",
  capture_pageleave: false,
  capture_exceptions: true,
  disable_session_recording: true,
  capture_performance: false,
  enable_recording_console_log: false,
  disable_surveys: true,
  disable_conversations: true,
  disable_product_tours: true,
  disable_external_dependency_loading: true,
  advanced_disable_flags: true,
  advanced_disable_toolbar_metrics: true,
  save_campaign_params: false,
  save_referrer: false,
  respect_dnt: true,
  person_profiles: "identified_only",
  before_send: sanitizeTelemetry,
};

export function initializeTelemetry() {
  const token = import.meta.env.VITE_PUBLIC_POSTHOG_PROJECT_TOKEN;
  const host = import.meta.env.VITE_PUBLIC_POSTHOG_HOST;
  // Both settings are required for operator opt-in; development works without them.
  if (token && host)
    posthog.init(token, { ...telemetryConfig, api_host: host });
}

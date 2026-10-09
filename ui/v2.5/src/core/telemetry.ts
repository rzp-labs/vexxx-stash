import posthog, { CaptureResult, PostHogConfig } from "posthog-js/no-external";
// Bundle the error parser locally; never load remote code into the private UI.
import "posthog-js/dist/exception-autocapture";
import {
  diagnosticContext,
  diagnosticMessage,
  diagnosticAccounting,
  diagnosticEntries,
  diagnosticLimits,
  privateDiagnosticValue,
  IDiagnosticAccounting,
} from "./telemetry-redaction";

const deliveryHealth = {
  rejectedEvents: 0,
  requestFailures: 0,
  captureFailures: 0,
  lastHTTPStatus: 0,
};
export function recordTelemetryCaptureFailure() {
  deliveryHealth.captureFailures += 1;
}
// Local health is an observation of SDK attempts, never a delivery receipt.
export const telemetryDeliveryHealth = () => ({ ...deliveryHealth });
function recordRequestFailure(response: { statusCode: number }) {
  deliveryHealth.requestFailures += 1;
  deliveryHealth.lastHTTPStatus = Number.isInteger(response.statusCode)
    ? response.statusCode
    : 0;
}
function record(
  value: unknown,
  accounting: IDiagnosticAccounting
): CaptureResult["properties"] {
  return Object.fromEntries(diagnosticEntries(value, accounting));
}
// These SDK fields contain browsing/media, person, campaign or raw source data,
// rather than diagnostics. Unknown technical properties use the shared redactor.
const excludedProperty =
  /^(?:\$(?:current_url|host|pathname|title|referrer|referring_domain|initial_.*|session_entry_.*|raw_user_agent|timezone|device_id|groups|set|set_once|exception_release(?:_version)?|exception_message|exception_type|exception_personURL|exception_issueURL|exception_eventURL|exception_captureURL|lib_custom_api_host)|utm_.*|\$feature.*|\$sentry.*)$/;
const rebuilt = new Set([
  "token",
  "$geoip_disable",
  "$lib",
  "$lib_version",
  "$process_person_profile",
  "$app_namespace",
  "$app_version",
  "$app_build",
  "$release_id",
  "app_revision",
  "screen",
  "distinct_id",
  "$user_id",
  "$anon_distinct_id",
  "$session_id",
  "$window_id",
  "$exception_list",
  "$exception_level",
  "telemetry_diagnostics",
  "telemetry_delivery",
]);

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
// Limits count retained diagnostics. Walk in source order and stop after fifty
// accepted bundle frames, so private/foreign/malformed frames cannot hide them.
function bundleFrames(
  value: unknown,
  accounting: IDiagnosticAccounting
): Record<string, unknown>[] {
  const frames: Record<string, unknown>[] = [];
  if (!Array.isArray(value)) return frames;
  if (value.length > diagnosticLimits.inspectedEntries)
    accounting.truncated += 1;
  for (
    let index = 0;
    index < Math.min(value.length, diagnosticLimits.inspectedEntries);
    index += 1
  ) {
    const descriptor = Object.getOwnPropertyDescriptor(value, String(index));
    if (descriptor && !("value" in descriptor)) {
      accounting.accessors += 1;
      continue;
    }
    const original = descriptor?.value;
    if (!original || typeof original !== "object") {
      accounting.omitted += 1;
      continue;
    }
    const frame = record(original, accounting);
    const filename = bundleFilename(frame.filename);
    if (!filename) {
      accounting.omitted += 1;
      continue;
    }
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
      /^[\p{L}\p{N}_$.[\]<># :()-]+$/u.test(frame.function) &&
      diagnosticMessage(frame.function) === frame.function
    ) {
      safe.function = frame.function.slice(0, 512);
      if (frame.function.length > 512) accounting.truncated += 1;
    }
    if (typeof frame.chunk_id === "string" && uuid.test(frame.chunk_id))
      safe.chunk_id = frame.chunk_id;
    frames.push(safe);
    if (frames.length === diagnosticLimits.frames) {
      if (index + 1 < value.length) accounting.truncated += 1;
      break;
    }
  }
  return frames;
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
  if (
    !event ||
    ![
      "$pageview",
      "$identify",
      "$exception",
      "$$client_ingestion_warning",
    ].includes(event.event)
  ) {
    deliveryHealth.rejectedEvents += 1;
    return null;
  }
  const accounting = diagnosticAccounting();
  const source = record(event.properties, accounting);
  const properties: CaptureResult["properties"] = {
    token: source.token, // The SDK's public ingestion token, never an upload key.
    $geoip_disable: true,
    $lib: "web",
    $lib_version: diagnosticContext(source.$lib_version, accounting),
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
    const role =
      record(event.$set, accounting).user_role ??
      record(source.$set, accounting).user_role;
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
  // The CLI/plugin injects this build-owned global; app_build is not a release ID.
  const injectedRelease = Object.getOwnPropertyDescriptor(
    globalThis,
    "_posthogReleaseId"
  )?.value;
  if (
    typeof injectedRelease === "string" &&
    injectedRelease.length > 0 &&
    injectedRelease.length <= 512 &&
    diagnosticMessage(injectedRelease) === injectedRelease
  )
    properties.$release_id = injectedRelease;
  if (["$exception", "$$client_ingestion_warning"].includes(event.event)) {
    let retainedProperties = 0;
    for (const [key, value] of diagnosticEntries(source, accounting)) {
      if (retainedProperties >= diagnosticLimits.objectFields) {
        accounting.truncated += 1;
        break;
      }
      if (rebuilt.has(key)) continue;
      if (excludedProperty.test(key)) {
        accounting.omitted += 1;
        continue;
      }
      // Screen/viewport sizes are useful for layout diagnostics but exact device
      // dimensions add fingerprinting precision. Retain hundred-pixel buckets.
      if (/^\$(?:screen|viewport)_(?:height|width)$/.test(key)) {
        if (
          typeof value === "number" &&
          Number.isFinite(value) &&
          value >= 0 &&
          value <= 100000
        ) {
          properties[key] = Math.round(value / 100) * 100;
          retainedProperties += 1;
        } else accounting.omitted += 1;
        continue;
      }
      if (privateDiagnosticValue(key, value)) {
        accounting.redacted += 1;
        properties[key] = "[private content redacted]";
        retainedProperties += 1;
        continue;
      }
      const safe = diagnosticContext(value, accounting);
      if (safe !== undefined) {
        properties[key] = safe;
        retainedProperties += 1;
      }
    }
    properties.telemetry_delivery = telemetryDeliveryHealth();
  }
  if (event.event === "$exception") {
    // Optional context must never spend the cause/frame inspection budget.
    // Both scopes remain bounded and contribute to the omission counters.
    const exceptionAccounting = diagnosticAccounting();
    properties.screen = diagnosticScreen();
    properties.app_revision = import.meta.env.VITE_APP_GITHASH || "development";
    if (
      ["fatal", "error", "warning", "log", "info", "debug"].includes(
        source.$exception_level
      )
    )
      properties.$exception_level = source.$exception_level;
    const exceptions: Record<string, unknown>[] = [];
    const incoming = Array.isArray(source.$exception_list)
      ? source.$exception_list
      : [];
    if (incoming.length > diagnosticLimits.inspectedEntries)
      exceptionAccounting.truncated += 1;
    for (
      let index = 0;
      index < Math.min(incoming.length, diagnosticLimits.inspectedEntries);
      index += 1
    ) {
      const descriptor = Object.getOwnPropertyDescriptor(
        incoming,
        String(index)
      );
      if (descriptor && !("value" in descriptor)) {
        exceptionAccounting.accessors += 1;
        continue;
      }
      const original = descriptor?.value;
      if (
        !original ||
        typeof original !== "object" ||
        Array.isArray(original)
      ) {
        exceptionAccounting.omitted += 1;
        continue;
      }
      const item = record(original, exceptionAccounting);
      const rawMechanism = record(item.mechanism, exceptionAccounting);
      const validType =
        typeof item.type === "string" &&
        /^[A-Za-z_$][\w.$]{0,79}$/.test(item.type);
      const mechanism = {
        ...(typeof rawMechanism.handled === "boolean"
          ? { handled: rawMechanism.handled }
          : {}),
        ...(typeof rawMechanism.synthetic === "boolean"
          ? { synthetic: rawMechanism.synthetic }
          : {}),
        ...(["generic", "chained", "onerror", "onunhandledrejection"].includes(
          rawMechanism.type
        )
          ? { type: rawMechanism.type }
          : {}),
        ...(["cause", "member"].includes(rawMechanism.source)
          ? { source: rawMechanism.source }
          : {}),
        ...Object.fromEntries(
          ["exception_id", "parent_id"].flatMap((key) =>
            Number.isSafeInteger(rawMechanism[key]) && rawMechanism[key] >= 0
              ? [[key, rawMechanism[key]]]
              : []
          )
        ),
      };
      const frames = bundleFrames(
        record(item.stacktrace, exceptionAccounting).frames,
        exceptionAccounting
      );
      // A manual envelope can omit a message and still carry useful diagnostics.
      // Check retained metadata once; empty/unsafe entries must not consume slots.
      if (
        !(typeof item.value === "string" && item.value.trim().length > 0) &&
        !validType &&
        !Object.keys(mechanism).length &&
        !frames.length
      ) {
        exceptionAccounting.omitted += 1;
        continue;
      }
      exceptions.push({
        ...Object.fromEntries(
          ["module", "thread_id"].flatMap((key) => {
            const safe = Object.prototype.hasOwnProperty.call(item, key)
              ? diagnosticContext(item[key], exceptionAccounting)
              : undefined;
            return safe === undefined ? [] : [[key, safe]];
          })
        ),
        type: validType
          ? diagnosticMessage(item.type, exceptionAccounting)
          : "Error",
        value: diagnosticMessage(item.value, exceptionAccounting),
        mechanism,
        stacktrace: { type: "raw", frames },
      });
      if (exceptions.length === diagnosticLimits.exceptions) {
        if (index + 1 < incoming.length) exceptionAccounting.truncated += 1;
        break;
      }
    }
    properties.$exception_list = exceptions;
    properties.telemetry_diagnostics = {
      ...Object.fromEntries(
        (
          [
            "redacted",
            "truncated",
            "omitted",
            "cycles",
            "accessors",
            "inspected",
          ] as const
        ).map((key) => [key, accounting[key] + exceptionAccounting[key]])
      ),
      inspection: {
        context: accounting.inspected,
        exceptions: exceptionAccounting.inspected,
        budget_per_scope: diagnosticLimits.inspectedProperties,
      },
      exceptions_supplied: incoming.length,
      exceptions_retained: exceptions.length,
      limits: diagnosticLimits,
      // These are pinned SDK upstream limits; before_send cannot count losses there.
      sdk_upstream_limits: { exceptions: 50, frames: 50, stack_lines: 1000 },
    };
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
  on_request_error: recordRequestFailure,
};

export function initializeTelemetry() {
  const token = import.meta.env.VITE_PUBLIC_POSTHOG_PROJECT_TOKEN;
  const host = import.meta.env.VITE_PUBLIC_POSTHOG_HOST;
  // Both settings are required for operator opt-in; development works without them.
  if (token && host)
    posthog.init(token, { ...telemetryConfig, api_host: host });
}

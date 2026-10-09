import { onError } from "@apollo/client/link/error";
import { DocumentNode, OperationDefinitionNode } from "graphql";
import posthog from "posthog-js/no-external";
import { recordTelemetryCaptureFailure } from "./telemetry";
function capture(error: Error, properties: Record<string, unknown>) {
  try {
    posthog.captureException(error, properties);
  } catch {
    recordTelemetryCaptureFailure();
  }
}

const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const knownCodes = new Set([
  // Shipped internal/api/error.go codes, plus standard GraphQL adapter codes.
  "NOT_SUPPORTED",
  "BAD_INPUT",
  "CANCELLED",
  "INTERNAL_ERROR",
  "UNKNOWN",
  "UNAUTHENTICATED",
  "FORBIDDEN",
  "BAD_USER_INPUT",
  "INTERNAL_SERVER_ERROR",
  "GRAPHQL_PARSE_FAILED",
  "GRAPHQL_VALIDATION_FAILED",
  "PERSISTED_QUERY_NOT_FOUND",
  "PERSISTED_QUERY_NOT_SUPPORTED",
  "NOT_FOUND",
  "CONFLICT",
]);

// Only names from shipped generated documents are diagnostic operation identity.
// A caller-supplied operation name or alias can contain private media/user data.
export function applicationOperationNames(documents: unknown[]): Set<string> {
  const names = new Set<string>();
  for (const document of documents) {
    if (
      !document ||
      typeof document !== "object" ||
      !("kind" in document) ||
      document.kind !== "Document"
    )
      continue;
    for (const definition of (document as DocumentNode).definitions) {
      if (definition.kind === "OperationDefinition" && definition.name)
        names.add(definition.name.value);
    }
  }
  return names;
}

export function createDiagnosticErrorLink(operationNames: ReadonlySet<string>) {
  const captured = new WeakSet<object>();
  return onError(({ graphQLErrors, networkError, operation }) => {
    const definition = operation.query.definitions.find(
      (item): item is OperationDefinitionNode =>
        item.kind === "OperationDefinition" &&
        item.name?.value === operation.operationName
    );
    const operationName = operationNames.has(operation.operationName)
      ? operation.operationName
      : "unrecognized";
    const context = {
      operation: operationName,
      stage: "graphql",
      operation_kind: definition?.operation ?? "unknown",
    };
    for (const error of graphQLErrors ?? []) {
      if (captured.has(error)) continue;
      captured.add(error);
      const extensions = error.extensions ?? {};
      const eventID = extensions.telemetry_event_id;
      if (
        extensions.telemetry_captured === true &&
        typeof eventID === "string" &&
        uuid.test(eventID)
      )
        continue;
      const code =
        typeof extensions.code === "string" && knownCodes.has(extensions.code)
          ? extensions.code
          : "UNCLASSIFIED";
      // Raw server messages may contain user data. Server capture owns full safe
      // diagnostics; this fallback describes an unmarked operation failure.
      capture(new Error(`GraphQL operation failed (${code})`), {
        ...context,
        code,
        ...(typeof eventID === "string" && uuid.test(eventID)
          ? { backend_event_id: eventID }
          : {}),
      });
    }
    if (networkError && !captured.has(networkError)) {
      captured.add(networkError);
      capture(networkError, {
        ...context,
        stage: "graphql.transport",
      });
    }
  });
}

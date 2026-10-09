import { afterAll, afterEach, describe, expect, it, vi } from "vitest";
const forbiddenFetch = vi.hoisted(() => {
  const fetch = vi.fn().mockRejectedValue(new Error("network forbidden"));
  vi.stubGlobal("fetch", fetch);
  return fetch;
});
import { ApolloLink, execute, gql } from "@apollo/client";
import { GraphQLWsLink } from "@apollo/client/link/subscriptions";
import { Client } from "graphql-ws";
import createUploadLink from "apollo-upload-client/createUploadLink.mjs";
import posthog, { PostHog, CaptureResult } from "posthog-js/no-external";
import { createDiagnosticErrorLink } from "./telemetry-api";
import { telemetryConfig } from "./telemetry";
// @ts-expect-error Node-only evidence writer; never included in the production bundle.
import { writeFileSync } from "node:fs";

const query = gql`
  subscription ScanProgress {
    privateAlias: scanProgress
  }
`;
const eventID = "11111111-2222-4333-8444-555555555555";
const run = (link: ApolloLink) =>
  new Promise<unknown>((resolve, reject) => {
    execute(link, {
      query,
      variables: { input: { name: "Jane Smith" } },
    }).subscribe({
      next: resolve,
      error: reject,
    });
  });
function sdkTransport() {
  const captured: CaptureResult[] = [];
  const client = new PostHog();
  vi.spyOn(client, "_send_retriable_request").mockImplementation((request) => {
    captured.push(request.data as CaptureResult);
  });
  vi.spyOn(posthog, "captureException").mockImplementation(
    (error, properties) => client.captureException(error, properties)
  );
  client.init("test-offline-apollo-token", {
    ...telemetryConfig,
    api_host: "https://example.invalid",
    persistence: "memory",
    bootstrap: { distinctID: "00000000-0000-4000-8000-000000000000" },
    capture_pageview: false,
    capture_exceptions: false,
    request_batching: false,
  });
  captured.length = 0;
  vi.spyOn(XMLHttpRequest.prototype, "send").mockImplementation(() => {
    throw new Error("network forbidden");
  });
  return captured;
}
afterAll(() => vi.unstubAllGlobals());
afterEach(() => {
  expect(forbiddenFetch).not.toHaveBeenCalled();
  expect(XMLHttpRequest.prototype.send).not.toHaveBeenCalled();
  vi.restoreAllMocks();
});
describe("installed Apollo producers through offline PostHog before_send", () => {
  it("keeps unmarked WebSocket diagnostics without exporting server message/cause or variables", async () => {
    const captured = sdkTransport();
    const errors = [
      {
        message: "Jane Smith",
        extensions: { telemetry_captured: true, telemetry_event_id: eventID },
      },
      {
        message: "Unlabelled private media name",
        extensions: { code: "BAD_INPUT" },
      },
    ];
    const subscribe: Client["subscribe"] = (_payload, sink) => {
      sink.error(errors);
      return () => {};
    };
    await expect(
      run(
        ApolloLink.from([
          createDiagnosticErrorLink(new Set(["ScanProgress"])),
          new GraphQLWsLink({ subscribe } as Client),
        ])
      )
    ).rejects.toMatchObject({ graphQLErrors: errors });
    await vi.waitFor(() => expect(captured).toHaveLength(1));
    const safe = captured[0];
    const evidencePath = (
      globalThis as typeof globalThis & {
        process?: { env: Record<string, string | undefined> };
      }
    ).process?.env.VEX80_R3_APOLLO_EVIDENCE;
    if (evidencePath)
      writeFileSync(evidencePath, `${JSON.stringify(safe, null, 2)}\n`);
    expect(JSON.stringify(safe)).not.toMatch(
      /Jane Smith|Unlabelled private media name|privateAlias|variables|input/
    );
    expect(safe.properties).toMatchObject({
      operation: "ScanProgress",
      operation_kind: "subscription",
      stage: "graphql",
      code: "BAD_INPUT",
    });
    expect(safe.properties.$exception_list).toMatchObject([
      { type: "Error", value: "GraphQL operation failed (BAD_INPUT)" },
    ]);
    expect(safe.properties.$exception_list).toHaveLength(1);
  });
  it.each([503, 200])(
    "protects actual HTTP response body at status %s",
    async (status) => {
      const captured = sdkTransport();
      const responseText = "Jane Smith private response";
      const fetch = vi
        .fn()
        .mockResolvedValue(new Response(responseText, { status }));
      let failure: Error | undefined;
      try {
        await run(
          ApolloLink.from([
            createDiagnosticErrorLink(new Set(["ScanProgress"])),
            createUploadLink({ fetch }),
          ])
        );
      } catch (error) {
        failure = error as Error;
      }
      expect(failure).toBeInstanceOf(Error);
      expect(failure?.name).toBe(
        status === 503 ? "ServerError" : "ServerParseError"
      );
      await vi.waitFor(() => expect(captured).toHaveLength(1));
      expect(captured[0].properties.stage).toBe("graphql.transport");
      expect(JSON.stringify(captured[0])).not.toMatch(
        /Jane Smith|private response|privateAlias|variables/
      );
      expect(fetch).toHaveBeenCalledTimes(1);
    }
  );
  it("preserves unfamiliar genuine fetch errors and technical causes", async () => {
    const captured = sdkTransport();
    const cause = new Error("Decoder queue stalled after 8 frames");
    const failure = new TypeError("Transport stream reset after 3 retries");
    Object.assign(failure, { cause });
    const fetch = vi.fn().mockRejectedValue(failure);
    await expect(
      run(
        ApolloLink.from([
          createDiagnosticErrorLink(new Set(["ScanProgress"])),
          createUploadLink({ fetch }),
        ])
      )
    ).rejects.toBe(failure);
    await vi.waitFor(() => expect(captured).toHaveLength(1));
    expect(captured[0].properties.$exception_list).toMatchObject([
      { type: "TypeError", value: failure.message },
      { type: "Error", value: cause.message },
    ]);
  });
});

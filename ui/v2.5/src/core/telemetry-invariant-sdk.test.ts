import { afterEach, describe, expect, it, vi } from "vitest";
import {
  ApolloClient,
  ApolloLink,
  InMemoryCache,
  Observable,
  gql,
} from "@apollo/client";
import { PostHog, CaptureResult } from "posthog-js/no-external";
import { diagnosticMessage } from "./telemetry-redaction";
import { telemetryConfig } from "./telemetry";

const prefix =
  "An error occurred! For more details, see the full error text at https://go.apollo.dev/c/err#";
const fallback = (detail: unknown) =>
  prefix + encodeURIComponent(JSON.stringify(detail));
afterEach(() => vi.restoreAllMocks());

describe("Apollo invariant diagnostics", () => {
  it("keeps only version and numeric identity, discarding all argument content", () => {
    const value = diagnosticMessage(
      fallback({
        version: "3.14.0",
        message: 42,
        args: ["Jane Smith", "Bearer private-token", { body: "private media" }],
      })
    );
    expect(value).toBe(
      "Apollo invariant 42 (client 3.14.0) [arguments redacted]"
    );
  });
  it.each([
    { version: "JaneSmithPrivateMedia", message: 42 },
    { version: "3.14.0", message: "JaneSmithPrivateMedia" },
    { version: "3.14.0", message: 1.5 },
    { version: "3.14.0", message: -1 },
    { version: "3.14.0", message: 100001 },
    [{ version: "3.14.0", message: 42 }],
  ])("rejects an untrusted invariant identity %#", (detail) => {
    const value = diagnosticMessage(fallback(detail));
    expect(value).not.toContain("Apollo invariant");
    expect(value).not.toMatch(/JaneSmithPrivateMedia|go.apollo.dev/);
  });
  it("handles malformed and oversized fallback payloads through normal redaction", () => {
    expect(diagnosticMessage(prefix + "%7Bbad%7D")).not.toContain(
      "go.apollo.dev"
    );
    const value = diagnosticMessage(
      fallback({
        version: "3.14.0",
        message: 42,
        args: ["JaneSmithPrivateMedia".repeat(10000)],
      })
    );
    expect(value).not.toMatch(
      /JaneSmithPrivateMedia|Apollo invariant|go.apollo.dev/
    );
  });
  it.each(["clearStore", "resetStore"] as const)(
    "reproduces %s cancellation with installed Apollo through offline SDK transport",
    async (method) => {
      const fetch = vi
        .spyOn(globalThis, "fetch")
        .mockRejectedValue(new Error("network forbidden"));
      const xhr = vi
        .spyOn(XMLHttpRequest.prototype, "send")
        .mockImplementation(() => {
          throw new Error("network forbidden");
        });
      const events: CaptureResult[] = [];
      const sdk = new PostHog();
      vi.spyOn(sdk, "_send_retriable_request").mockImplementation((request) => {
        events.push(request.data as CaptureResult);
      });
      sdk.init("test-offline-invariant-token", {
        ...telemetryConfig,
        api_host: "https://example.invalid",
        persistence: "memory",
        bootstrap: { distinctID: "00000000-0000-4000-8000-000000000000" },
        capture_pageview: false,
        capture_exceptions: false,
        request_batching: false,
      });
      let started = false;
      const client = new ApolloClient({
        cache: new InMemoryCache(),
        link: new ApolloLink(
          () =>
            new Observable(() => {
              started = true;
            })
        ),
      });
      const pending = client
        .query({
          query: gql`
            query DiagnosticPending {
              __typename
            }
          `,
          fetchPolicy: "network-only",
        })
        .catch((error: unknown) => error);
      await vi.waitFor(() => expect(started).toBe(true));
      await client[method]();
      const failure = await pending;
      expect(failure).toBeInstanceOf(Error);
      expect((failure as Error).message).toContain(
        "https://go.apollo.dev/c/err#"
      );
      expect(diagnosticMessage((failure as Error).message)).toBe(
        "Apollo invariant 42 (client 3.14.0) [arguments redacted]"
      );
      sdk.captureException(failure);
      await vi.waitFor(() => expect(events).toHaveLength(1));
      expect(events[0].properties.$exception_list).toMatchObject([
        { value: "Apollo invariant 42 (client 3.14.0) [arguments redacted]" },
      ]);
      expect(fetch).not.toHaveBeenCalled();
      expect(xhr).not.toHaveBeenCalled();
      client.stop();
    }
  );
});

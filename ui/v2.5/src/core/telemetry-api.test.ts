import { describe, expect, it, vi, beforeEach } from "vitest";
import { ApolloLink, execute, gql, Observable } from "@apollo/client";
import { telemetryDeliveryHealth } from "./telemetry";
import { GraphQLError } from "graphql";
import posthog from "posthog-js/no-external";
import {
  applicationOperationNames,
  createDiagnosticErrorLink,
} from "./telemetry-api";
vi.mock("posthog-js/no-external", () => ({
  default: { captureException: vi.fn() },
}));
const document = gql`
  query FindScenes($privateVariable: String) {
    privateAlias: findScenes(filter: $privateVariable) {
      count
    }
  }
`;
const eventID = "11111111-2222-4333-8444-555555555555";
const error = (extensions = {}) =>
  new GraphQLError("Private server response Jane Smith", { extensions });
const run = (link: ApolloLink, query = document) =>
  new Promise<unknown[]>((resolve, reject) => {
    const received: unknown[] = [];
    execute(link, {
      query,
      variables: { privateVariable: "private media name" },
    }).subscribe({
      next: (value) => received.push(value),
      complete: () => resolve(received),
      error: reject,
    });
  });
const responseLink = (errors: GraphQLError[]) =>
  new ApolloLink(
    () =>
      new Observable((observer) => {
        observer.next({ errors });
        observer.complete();
      })
  );
beforeEach(() => vi.clearAllMocks());
describe("Apollo diagnostic contract", () => {
  it("suppresses only successfully enqueued marked errors in a mixed response", async () => {
    const errors = [
      error({ telemetry_captured: true, telemetry_event_id: eventID }),
      error({ code: "INTERNAL_SERVER_ERROR" }),
      error({ telemetry_captured: true }),
      error({ telemetry_captured: false, telemetry_event_id: eventID }),
      error({
        telemetry_captured: true,
        telemetry_event_id: "private-invalid",
      }),
    ];
    const originalExtensions = errors.map((graphQLError) => ({
      ...graphQLError.extensions,
    }));
    const link = ApolloLink.from([
      createDiagnosticErrorLink(applicationOperationNames([document])),
      responseLink(errors),
    ]);
    expect(await run(link)).toEqual([{ errors }]);
    expect(posthog.captureException).toHaveBeenCalledTimes(4);
    expect(errors.map((graphQLError) => graphQLError.extensions)).toEqual(
      originalExtensions
    );
    expect(errors[0].extensions).toEqual({
      telemetry_captured: true,
      telemetry_event_id: eventID,
    });
    expect(posthog.captureException).toHaveBeenCalledWith(
      expect.any(Error),
      expect.objectContaining({
        operation: "FindScenes",
        code: "INTERNAL_SERVER_ERROR",
      })
    );
    const serialized = JSON.stringify(
      vi
        .mocked(posthog.captureException)
        .mock.calls.map(([exception, context]) => ({
          message: exception instanceof Error ? exception.message : exception,
          context,
        }))
    );
    expect(serialized).not.toMatch(
      /Jane Smith|privateAlias|privateVariable|private media|private-invalid|findScenes/
    );
  });
  it.each([
    {},
    { telemetry_captured: false },
    { telemetry_captured: "true", telemetry_event_id: eventID },
    { telemetry_captured: true, telemetry_event_id: 42 },
  ])(
    "captures unmarked/failed/malformed enqueue metadata %j",
    async (extensions) => {
      await run(
        ApolloLink.from([
          createDiagnosticErrorLink(new Set(["FindScenes"])),
          responseLink([error(extensions)]),
        ])
      );
      expect(posthog.captureException).toHaveBeenCalledTimes(1);
    }
  );
  it("retains transport errors and deduplicates the same failure without changing propagation", async () => {
    const failure = new TypeError("Failed to fetch");
    const link = ApolloLink.from([
      createDiagnosticErrorLink(new Set(["FindScenes"])),
      new ApolloLink(
        () => new Observable((observer) => observer.error(failure))
      ),
    ]);
    await expect(run(link)).rejects.toBe(failure);
    await expect(run(link)).rejects.toBe(failure);
    expect(posthog.captureException).toHaveBeenCalledExactlyOnceWith(failure, {
      operation: "FindScenes",
      operation_kind: "query",
      stage: "graphql.transport",
    });
  });
  it("deduplicates shared error objects across subscriptions", async () => {
    const errors = [error()];
    const link = ApolloLink.from([
      createDiagnosticErrorLink(new Set(["FindScenes"])),
      responseLink(errors),
    ]);
    await run(link);
    await run(link);
    expect(posthog.captureException).toHaveBeenCalledTimes(1);
  });
  it("keeps response propagation working if SDK capture throws", async () => {
    const before = telemetryDeliveryHealth();
    vi.mocked(posthog.captureException).mockImplementationOnce(() => {
      throw new Error("SDK failure");
    });
    const errors = [error()];
    expect(
      await run(
        ApolloLink.from([
          createDiagnosticErrorLink(new Set(["FindScenes"])),
          responseLink(errors),
        ])
      )
    ).toEqual([{ errors }]);
    expect(telemetryDeliveryHealth().captureFailures).toBe(
      before.captureFailures + 1
    );
  });
  it("does not export arbitrary operation names or error codes", async () => {
    const query = gql`
      query JaneSmithPrivateMedia {
        result
      }
    `;
    await run(
      ApolloLink.from([
        createDiagnosticErrorLink(
          applicationOperationNames([document, null, "ignore"])
        ),
        responseLink([error({ code: "JaneSmithPrivateMedia" })]),
      ]),
      query
    );
    expect(posthog.captureException).toHaveBeenCalledWith(expect.any(Error), {
      operation: "unrecognized",
      operation_kind: "query",
      stage: "graphql",
      code: "UNCLASSIFIED",
    });
  });
});

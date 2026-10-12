import {
  ApolloClient,
  ApolloLink,
  ApolloProvider,
  HttpLink,
  InMemoryCache,
  Observable,
} from "@apollo/client";
import { act, render, screen, within } from "@testing-library/react";
import {
  DocumentNode,
  FragmentDefinitionNode,
  SelectionSetNode,
} from "graphql";
import { IntlProvider } from "react-intl";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import * as GQL from "src/core/generated-graphql";
import locale from "src/locales/en-GB.json";
import { IntelGenerationSettings } from "./SettingsSystemPanel";
import { useGenerationTuningRefresh } from "./useGenerationTuningRefresh";

vi.mock("./context", () => ({
  useSettings: () => ({ advancedMode: false }),
  useSettingsOptional: () => ({ advancedMode: false }),
}));
const ws = vi.hoisted(() => ({
  connected: undefined as (() => void) | undefined,
}));
vi.mock("src/core/StashService", async (importOriginal) => ({
  ...(await importOriginal<typeof import("src/core/StashService")>()),
  getWSClient: () => ({
    on: (_event: string, listener: () => void) => {
      ws.connected = listener;
      return () => {
        ws.connected = undefined;
      };
    },
  }),
}));
function flatten(obj: object, prefix = ""): Record<string, string> {
  return Object.fromEntries(
    Object.entries(obj).flatMap(([key, value]) => {
      const path = prefix ? `${prefix}.${key}` : key;
      return typeof value === "string"
        ? [[path, value]]
        : Object.entries(flatten(value, path));
    })
  );
}
// Include the actual operation's other selections so Apollo gets a complete cache
// snapshot. Only generation values are meaningful in this transport fixture.
function selectedShape(document: DocumentNode) {
  const fragments = new Map(
    document.definitions
      .filter(
        (d): d is FragmentDefinitionNode => d.kind === "FragmentDefinition"
      )
      .map((d) => [d.name.value, d])
  );
  function shape(selection: SelectionSetNode): Record<string, unknown> {
    const result: Record<string, unknown> = {};
    for (const field of selection.selections) {
      if (field.kind === "Field")
        result[field.alias?.value ?? field.name.value] = field.selectionSet
          ? shape(field.selectionSet)
          : null;
      if (field.kind === "FragmentSpread") {
        const fragment = fragments.get(field.name.value)!;
        Object.assign(result, shape(fragment.selectionSet), {
          __typename: fragment.typeCondition.name.value,
        });
      }
    }
    return result;
  }
  const operation = document.definitions.find(
    (d) => d.kind === "OperationDefinition"
  )!;
  if (operation.kind !== "OperationDefinition")
    throw new Error("operation missing");
  return shape(operation.selectionSet);
}
function configuration(value: number | null) {
  const result = selectedShape(GQL.ConfigurationDocument);
  const config = result.configuration as Record<string, unknown>;
  const general = config.general as Record<string, unknown>;
  Object.assign(general, {
    generationMarkerBackend: "software",
    generationSpriteBackend: "vaapi",
    generationPreviewBackend: "vaapi",
    generationDevice: "/dev/dri/renderD128",
    generationBudgetEnabled: false,
    generationMaxProcesses: 0,
    generationMaxGPUProcesses: 0,
    generationThreads: 0,
    generationRestartRequired: false,
    generationConfigurationError: null,
    activeGeneration: {
      __typename: "GenerationConfiguration",
      markerBackend: "software",
      spriteBackend: "vaapi",
      previewBackend: "vaapi",
      device: "/dev/dri/renderD128",
      budgetEnabled: false,
      maxProcesses: 8,
      maxGPUProcesses: 4,
      threads: 1,
    },
    generationTuning: {
      __typename: "GenerationTuning",
      sprite: {
        __typename: "GenerationWorkloadTuning",
        automaticConcurrency: value,
        measurementState: value ? "MEASURED" : "NOT_MEASURED",
      },
      preview: {
        __typename: "GenerationWorkloadTuning",
        automaticConcurrency: value,
        measurementState: value ? "MEASURED" : "ADAPTING",
      },
    },
  });
  return result;
}
function ChangingRefetchIdentity() {
  const { data, refetch, loading, error } = GQL.useConfigurationQuery({
    fetchPolicy: "cache-and-network",
    notifyOnNetworkStatusChange: true,
  });
  // A new wrapper on every render must not replace the scheduler or its budget.
  const stale = useGenerationTuningRefresh(() => refetch(), loading);
  return (
    <span data-testid="freshness">
      {stale || loading || error
        ? "Adapting"
        : data?.configuration.general.generationTuning.sprite
            .automaticConcurrency}
    </span>
  );
}
function fixture() {
  let latest = configuration(null);
  let events:
    | { next: (value: { data: Record<string, unknown> }) => void }
    | undefined;
  const pending: (() => void)[] = [];
  let hold = false;
  let queries = 0;
  let active = 0;
  let peak = 0;
  let subscriptionClosed = false;
  let failures = 0;
  let failureKind: "http" | "graphql" | "incomplete" = "http";
  const link = ApolloLink.split(
    (operation) => operation.operationName === "JobsSubscribe",
    new ApolloLink(
      () =>
        new Observable((observer) => {
          events = observer;
          return () => {
            subscriptionClosed = true;
            events = undefined;
          };
        })
    ),
    // Exercise Apollo's actual HTTP rejection, query updates and retained cache.
    // Only transport and websocket delivery are controlled by this fixture.
    new HttpLink({
      uri: "http://localhost/graphql",
      fetch: () => {
        queries++;
        active++;
        peak = Math.max(peak, active);
        const response = latest;
        const failed = failures > 0;
        const kind = failureKind;
        if (failed) failures--;
        const config = response.configuration as Record<string, unknown>;
        const body =
          failed && kind === "incomplete"
            ? {
                data: {
                  ...response,
                  configuration: {
                    ...config,
                    general: {
                      ...(config.general as object),
                      generationTuning: null,
                    },
                  },
                },
              }
            : failed
            ? { errors: [{ message: "Generation tuning unavailable" }] }
            : { data: response };
        return new Promise<Response>((resolve) => {
          const finish = () => {
            active--;
            resolve(
              new Response(JSON.stringify(body), {
                status: failed && kind === "http" ? 503 : 200,
                headers: { "Content-Type": "application/json" },
              })
            );
          };
          if (hold) pending.push(finish);
          else finish();
        });
      },
    })
  );
  const client = new ApolloClient({ cache: new InMemoryCache(), link });
  client.writeQuery({ query: GQL.ConfigurationDocument, data: latest });
  const element = (changingRefetchIdentity = false) => (
    <ApolloProvider client={client}>
      <IntlProvider locale="en-GB" messages={flatten(locale)}>
        {changingRefetchIdentity ? (
          <ChangingRefetchIdentity />
        ) : (
          <IntelGenerationSettings />
        )}
      </IntlProvider>
    </ApolloProvider>
  );
  const event = (
    description = "Generating...",
    status = "RUNNING",
    type = "UPDATE",
    id = "generation-1",
    progress = 0.5
  ) => {
    events?.next({
      data: {
        jobsSubscribe: {
          __typename: "JobStatusUpdate",
          type,
          job: {
            __typename: "Job",
            id,
            description,
            status,
            subTasks: [],
            progress,
            processed: 1,
            total: 2,
            error: null,
            startTime: null,
          },
        },
      },
    });
  };
  return {
    client,
    element,
    event,
    setValue: (value: number | null) => {
      latest = configuration(value);
    },
    hold: (v: boolean) => {
      hold = v;
    },
    finish: () => pending.shift()?.(),
    failNext: (count = 1, kind: typeof failureKind = "http") => {
      failures = count;
      failureKind = kind;
    },
    queries: () => queries,
    peak: () => peak,
    closed: () => subscriptionClosed,
  };
}
async function flush() {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
}
async function advance() {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(5000);
  });
}
function row(kind: "sprite" | "preview") {
  return within(document.getElementById(`generation-${kind}-concurrency`)!);
}
beforeEach(() => {
  vi.useFakeTimers();
  Object.defineProperty(document, "hidden", {
    configurable: true,
    value: false,
  });
});
afterEach(() => {
  vi.restoreAllMocks();
  vi.useRealTimers();
});

describe("real Apollo generation tuning refresh", () => {
  it("keeps a measured number stable through spaced progress updates and refreshes only at completion", async () => {
    const f = fixture();
    f.setValue(2);
    const view = render(f.element());
    await flush();
    f.setValue(3);
    for (let i = 0; i < 10; i++) {
      act(() =>
        f.event(
          "Generating...",
          "RUNNING",
          "UPDATE",
          "generation-1",
          (i + 1) / 11
        )
      );
      await act(async () => {
        await vi.advanceTimersByTimeAsync(3000);
      });
      expect(f.queries()).toBe(1);
      expect(row("sprite").getByText("2")).toBeInTheDocument();
      expect(row("sprite").queryByText("Adapting")).not.toBeInTheDocument();
    }
    act(() => f.event("Generating...", "FINISHED", "REMOVE"));
    await advance();
    expect(f.queries()).toBe(2);
    expect(row("sprite").getByText("3")).toBeInTheDocument();
    view.unmount();
    f.client.stop();
  });
  it.each(["http", "graphql", "incomplete"] as const)(
    "withholds cached numbers after a failed final-event refetch (%s) and recovers without reconnecting",
    async (kind) => {
      const f = fixture();
      f.setValue(2);
      const view = render(f.element());
      await flush();
      expect(row("sprite").getByText("2")).toBeInTheDocument();
      expect(row("preview").getByText("2")).toBeInTheDocument();
      f.setValue(3);
      f.failNext(1, kind);
      act(() => f.event("Generating...", "FINISHED", "REMOVE"));
      await advance();
      expect(f.queries()).toBe(2);
      expect(ws.connected).toBeDefined();
      if (kind !== "incomplete")
        expect(
          f.client.readQuery<GQL.ConfigurationQuery>({
            query: GQL.ConfigurationDocument,
          })?.configuration.general.generationTuning.sprite.automaticConcurrency
        ).toBe(2);
      for (const workload of ["sprite", "preview"] as const) {
        expect(row(workload).queryByText("2")).not.toBeInTheDocument();
        expect(row(workload).getByText("Adapting")).toBeInTheDocument();
      }
      await advance();
      expect(f.queries()).toBe(3);
      expect(row("sprite").getByText("3")).toBeInTheDocument();
      expect(row("preview").getByText("3")).toBeInTheDocument();
      await act(async () => {
        await vi.advanceTimersByTimeAsync(60000);
      });
      expect(f.queries()).toBe(3);
      view.unmount();
      f.client.stop();
    }
  );
  it("bounds failed recovery even with event bursts and loading transitions, then accepts a new trigger", async () => {
    const f = fixture();
    f.setValue(2);
    const view = render(f.element());
    await flush();
    f.setValue(3);
    f.failNext(3);
    act(() => f.event("Generating...", "FINISHED", "REMOVE"));
    await advance();
    act(() => {
      for (let i = 0; i < 20; i++) f.event();
    });
    await advance();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10000);
    });
    expect(f.queries()).toBe(4); // Entry plus three attempts in the recovery cycle.
    expect(f.peak()).toBe(1);
    for (let i = 0; i < 10; i++) {
      act(() =>
        f.event(
          "Generating...",
          "RUNNING",
          "UPDATE",
          "generation-1",
          (i + 1) / 11
        )
      );
      await act(async () => {
        await vi.advanceTimersByTimeAsync(3000);
      });
      expect(f.queries()).toBe(4);
    }
    for (const kind of ["sprite", "preview"] as const) {
      expect(row(kind).queryByText("2")).not.toBeInTheDocument();
      expect(row(kind).getByText("Adapting")).toBeInTheDocument();
    }
    view.rerender(f.element());
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60000);
    });
    expect(f.queries()).toBe(4);
    act(() => f.event("Scanning...", "FAILED", "REMOVE"));
    await advance();
    expect(f.queries()).toBe(5);
    expect(row("sprite").getByText("3")).toBeInTheDocument();
    view.unmount();
    f.client.stop();
  });
  it("cancels a failed-refetch retry and its listeners on unmount", async () => {
    const f = fixture();
    const view = render(f.element());
    await flush();
    f.failNext();
    act(() => f.event("Generating...", "FINISHED", "REMOVE"));
    await advance();
    expect(f.queries()).toBe(2);
    view.unmount();
    expect(ws.connected).toBeUndefined();
    act(() => {
      document.dispatchEvent(new Event("visibilitychange"));
      f.event();
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60000);
    });
    expect(f.queries()).toBe(2);
    expect(f.closed()).toBe(true);
    f.client.stop();
  });
  it.each([false, true])(
    "ignores an in-flight HTTP completion after unmount (failure: %s)",
    async (failed) => {
      const f = fixture();
      const view = render(f.element());
      await flush();
      f.hold(true);
      if (failed) f.failNext();
      act(() => f.event("Generating...", "FINISHED", "REMOVE"));
      await advance();
      expect(f.queries()).toBe(2);
      const errors = vi.spyOn(console, "error");
      view.unmount();
      await flush();
      act(() => f.finish());
      await act(async () => {
        await vi.advanceTimersByTimeAsync(60000);
      });
      expect(f.queries()).toBe(2);
      expect(f.closed()).toBe(true);
      expect(ws.connected).toBeUndefined();
      expect(
        errors.mock.calls.some((args) =>
          /unmounted component/i.test(String(args[0]))
        )
      ).toBe(false);
      errors.mockRestore();
      f.client.stop();
    }
  );
  it("retains the bounded retry cycle when refetch's identity changes on every render", async () => {
    const f = fixture();
    f.setValue(2);
    const view = render(f.element(true));
    await flush();
    expect(screen.getByTestId("freshness")).toHaveTextContent("2");
    f.failNext(3);
    act(() => f.event("Generating...", "FINISHED", "REMOVE"));
    await advance();
    await advance();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10000);
    });
    view.rerender(f.element(true));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60000);
    });
    expect(f.queries()).toBe(4);
    expect(screen.getByTestId("freshness")).toHaveTextContent("Adapting");
    view.unmount();
    f.client.stop();
  });
  it("fetches on entry despite a startup cache hit and fetches again on reopening", async () => {
    const f = fixture();
    f.setValue(2);
    const view = render(f.element());
    await flush();
    expect(f.queries()).toBe(1);
    expect(row("sprite").getByText("2")).toBeInTheDocument();
    view.unmount();
    f.setValue(3);
    const reopened = render(f.element());
    await flush();
    expect(f.queries()).toBe(2);
    expect(row("sprite").getByText("3")).toBeInTheDocument();
    reopened.unmount();
    f.client.stop();
  });
  it("updates the actual query/cache on generation completion, coalesces bursts and does not poll", async () => {
    const f = fixture();
    const view = render(f.element());
    await flush();
    expect(row("sprite").getByText("Adapting")).toBeInTheDocument();
    f.setValue(2);
    act(() => {
      for (let i = 0; i < 20; i++) f.event();
      f.event("Generating...", "FINISHED", "REMOVE");
    });
    await advance();
    expect(f.queries()).toBe(2);
    expect(row("sprite").getByText("2")).toBeInTheDocument();
    expect(
      f.client.readQuery<GQL.ConfigurationQuery>({
        query: GQL.ConfigurationDocument,
      })?.configuration.general.generationTuning.sprite.automaticConcurrency
    ).toBe(2);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60000);
    });
    expect(f.queries()).toBe(2);
    act(() => f.event("Exporting..."));
    await advance();
    expect(f.queries()).toBe(2);
    f.setValue(null);
    act(() => f.event("Scanning...", "FAILED", "REMOVE"));
    await advance();
    expect(row("sprite").getByText("Adapting")).toBeInTheDocument();
    view.unmount();
    await advance();
    expect(f.closed()).toBe(true);
    f.client.stop();
  });
  it("keeps one request in flight and follows events received during it", async () => {
    const f = fixture();
    const view = render(f.element());
    await flush();
    f.setValue(2);
    f.hold(true);
    act(() => f.event("Generating...", "FINISHED", "REMOVE"));
    await advance();
    f.setValue(3);
    act(() => f.event("Generating...", "FINISHED", "REMOVE", "generation-2"));
    await advance();
    expect(f.queries()).toBe(2);
    expect(f.peak()).toBe(1);
    act(() => f.finish());
    await flush();
    expect(row("sprite").queryByText("2")).not.toBeInTheDocument();
    expect(row("sprite").getByText("Adapting")).toBeInTheDocument();
    await advance();
    expect(f.queries()).toBe(3);
    act(() => f.finish());
    await flush();
    expect(row("sprite").getByText("3")).toBeInTheDocument();
    view.unmount();
    f.client.stop();
  });
  it("does not lose completion events during the panel-entry request", async () => {
    const f = fixture();
    f.hold(true);
    const view = render(f.element());
    await flush();
    f.setValue(3);
    act(() => f.event("Generating...", "FINISHED", "REMOVE"));
    await advance();
    expect(f.queries()).toBe(1);
    act(() => f.finish());
    await flush();
    await advance();
    expect(f.queries()).toBe(2);
    expect(f.peak()).toBe(1);
    act(() => f.finish());
    await flush();
    expect(row("sprite").getByText("3")).toBeInTheDocument();
    view.unmount();
    f.client.stop();
  });
  it("refreshes on return/reconnect and cancels queued work when leaving", async () => {
    const f = fixture();
    const view = render(f.element());
    await flush();
    Object.defineProperty(document, "hidden", {
      configurable: true,
      value: true,
    });
    f.setValue(2);
    act(() => f.event("Generating...", "FINISHED", "REMOVE"));
    await advance();
    expect(f.queries()).toBe(1);
    Object.defineProperty(document, "hidden", {
      configurable: true,
      value: false,
    });
    act(() => {
      document.dispatchEvent(new Event("visibilitychange"));
    });
    await advance();
    expect(row("sprite").getByText("2")).toBeInTheDocument();
    f.setValue(3);
    act(() => ws.connected?.());
    await advance();
    expect(row("sprite").getByText("3")).toBeInTheDocument();
    act(() => f.event("Generating...", "FINISHED", "REMOVE", "generation-2"));
    view.unmount();
    await advance();
    expect(f.queries()).toBe(3);
    expect(ws.connected).toBeUndefined();
    f.client.stop();
  });
});

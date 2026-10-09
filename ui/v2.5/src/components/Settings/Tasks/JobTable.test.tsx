import { act, render, screen } from "@testing-library/react";
import { print } from "graphql";
import { IntlProvider } from "react-intl";
import { beforeEach, describe, expect, it, vi } from "vitest";
import * as GQL from "src/core/generated-graphql";
import { JobTable } from "./JobTable";

const mocks = vi.hoisted(() => ({
  queue: undefined as unknown,
  event: undefined as unknown,
  refetch: vi.fn().mockResolvedValue({}),
  startPolling: vi.fn(),
  stopPolling: vi.fn(),
  dispose: vi.fn(),
  connected: undefined as undefined | (() => void),
}));
vi.mock("src/core/StashService", () => ({
  useJobQueue: () => ({
    data: mocks.queue,
    loading: false,
    refetch: mocks.refetch,
    startPolling: mocks.startPolling,
    stopPolling: mocks.stopPolling,
  }),
  useJobsSubscribe: () => ({ data: mocks.event }),
  getWSClient: () => ({
    on: (_event: string, cb: () => void) => {
      mocks.connected = cb;
      return mocks.dispose;
    },
  }),
  mutateStopJob: vi.fn(),
}));
vi.mock("src/core/generated-graphql", async (importOriginal) => ({
  ...(await importOriginal<typeof import("src/core/generated-graphql")>()),
  useSystemStatsQuery: () => ({ data: undefined }),
}));
vi.mock("src/utils/date", () => ({ formatRelativeTime: () => "now" }));
function job(
  overrides: Partial<GQL.JobDataFragment> = {}
): GQL.JobDataFragment {
  return {
    __typename: "Job",
    id: "1",
    status: GQL.JobStatus.Running,
    description: "Importing library",
    progress: 0.6,
    processed: 720,
    total: 1200,
    subTasks: ["same sampled activity"],
    addTime: "2026-10-09T18:00:00Z",
    ...overrides,
  };
}
function table() {
  return (
    <IntlProvider locale="en-GB">
      <JobTable />
    </IntlProvider>
  );
}
function update(
  value: GQL.JobDataFragment,
  type = GQL.JobStatusUpdateType.Update
) {
  mocks.event = { jobsSubscribe: { type, job: value } };
}
beforeEach(() => {
  vi.clearAllMocks();
  localStorage.clear();
  mocks.queue = { jobQueue: [job()] };
  mocks.event = undefined;
});
describe("authoritative work-unit progress", () => {
  it("requests actual counts in queue, find and subscription operations", () => {
    for (const document of [
      GQL.JobQueueDocument,
      GQL.FindJobDocument,
      GQL.JobsSubscribeDocument,
    ]) {
      expect(print(document)).toContain("processed");
      expect(print(document)).toContain("total");
    }
  });
  it("renders >500 and 1200 from snapshots separately from sampled activity", () => {
    localStorage.setItem(
      "job-history-1",
      JSON.stringify(Array.from({ length: 1200 }, (_, n) => `activity ${n}`))
    );
    const { rerender } = render(table());
    expect(
      screen.getByText("720 / 1200 work units processed")
    ).toBeInTheDocument();
    expect(
      screen.getByText("Recent activity (up to 500 sampled messages)")
    ).toBeInTheDocument();
    expect(screen.queryByText("#")).not.toBeInTheDocument();
    expect(screen.queryByText("ST")).not.toBeInTheDocument();
    expect(screen.queryByText("500")).not.toBeInTheDocument();
    expect(screen.queryByText("activity 699")).not.toBeInTheDocument();
    expect(screen.getByText("activity 1199")).toBeInTheDocument();
    expect(JSON.parse(localStorage.getItem("job-history-1")!)).toHaveLength(
      500
    );
    update(job({ processed: 1200, progress: 1 }));
    rerender(table());
    expect(
      screen.getByText("1200 / 1200 work units processed")
    ).toBeInTheDocument();
    expect(screen.getAllByText("same sampled activity")).toHaveLength(1);
  });
  it("accepts growing totals and preserves partial cancellation", () => {
    const { rerender } = render(table());
    update(job({ total: 2400, progress: 0.3 }));
    rerender(table());
    expect(
      screen.getByText("720 / 2400 work units processed")
    ).toBeInTheDocument();
    expect(screen.getByText("30%")).toBeInTheDocument();
    update(
      job({ status: GQL.JobStatus.Cancelled, total: 2400, progress: 0.3 }),
      GQL.JobStatusUpdateType.Remove
    );
    rerender(table());
    expect(screen.getByText("Cancelled")).toBeInTheDocument();
    expect(
      screen.getByText("720 / 2400 work units processed")
    ).toBeInTheDocument();
    expect(screen.queryByText("Done")).not.toBeInTheDocument();
    update(job({ processed: 600 }));
    rerender(table());
    expect(screen.getByText("Cancelled")).toBeInTheDocument();
    expect(
      screen.getByText("720 / 2400 work units processed")
    ).toBeInTheDocument();
  });
  it("does not invent full counts for a finished or failed job", () => {
    const { rerender } = render(table());
    update(
      job({ status: GQL.JobStatus.Finished, processed: 900 }),
      GQL.JobStatusUpdateType.Remove
    );
    rerender(table());
    expect(screen.getByText("Done")).toBeInTheDocument();
    expect(
      screen.getByText("900 / 1200 work units processed")
    ).toBeInTheDocument();
    update(
      job({
        id: "2",
        status: GQL.JobStatus.Failed,
        processed: 501,
        error: "Import failed",
      }),
      GQL.JobStatusUpdateType.Remove
    );
    rerender(table());
    expect(screen.getByText("Failed")).toBeInTheDocument();
    expect(
      screen.getByText("501 / 1200 work units processed")
    ).toBeInTheDocument();
    expect(screen.getByText("Import failed")).toBeInTheDocument();
  });

  it("renders counts above GraphQL Int and suppresses unsafe JavaScript integers", () => {
    mocks.queue = {
      jobQueue: [
        job({ processed: 2147483648, total: 4294967296, progress: 0.5 }),
      ],
    };
    const { rerender } = render(table());
    expect(
      screen.getByText("2147483648 / 4294967296 work units processed")
    ).toBeInTheDocument();
    update(
      job({
        processed: Number.MAX_SAFE_INTEGER + 1,
        total: Number.MAX_SAFE_INTEGER + 1,
      })
    );
    rerender(table());
    expect(
      screen.getByText("Work-unit counts exceed exact display range")
    ).toBeInTheDocument();
    expect(screen.queryByText(/9007199254740992/)).not.toBeInTheDocument();
    update(job({ processed: 720, total: Number.MAX_SAFE_INTEGER + 1 }));
    rerender(table());
    expect(
      screen.getByText("Work-unit counts exceed exact display range")
    ).toBeInTheDocument();
  });

  it("handles unknown totals and percentage-only jobs", () => {
    mocks.queue = { jobQueue: [job({ total: null, progress: null })] };
    const { rerender } = render(table());
    expect(
      screen.getByText("720 work units processed (total unknown)")
    ).toBeInTheDocument();
    update(job({ processed: null, total: null, progress: 0.9 }));
    rerender(table());
    expect(screen.queryByText(/work units processed/)).not.toBeInTheDocument();
    expect(screen.getByText("90%")).toBeInTheDocument();
  });
  it("recovers counts on remount and reconnect and polls for missed events", async () => {
    const first = render(table());
    first.unmount();
    mocks.queue = {
      jobQueue: [job({ processed: 1100, progress: 1100 / 1200 })],
    };
    const second = render(table());
    expect(
      screen.getByText("1100 / 1200 work units processed")
    ).toBeInTheDocument();
    expect(mocks.startPolling).toHaveBeenCalledWith(5000);
    await act(async () => mocks.connected!());
    expect(mocks.refetch).toHaveBeenCalledOnce();
    mocks.queue = { jobQueue: [job({ processed: 1200, progress: 1 })] };
    second.rerender(table());
    expect(
      screen.getByText("1200 / 1200 work units processed")
    ).toBeInTheDocument();
    second.unmount();
    expect(mocks.dispose).toHaveBeenCalledTimes(2);
    expect(mocks.stopPolling).toHaveBeenCalledTimes(2);
  });
  it("upserts concurrent jobs and does not reset subscriptions on hook rerenders", () => {
    const { rerender } = render(table());
    update(
      job({ id: "2", description: "Second import", processed: 501 }),
      GQL.JobStatusUpdateType.Add
    );
    rerender(table());
    expect(
      screen.getByText("501 / 1200 work units processed")
    ).toBeInTheDocument();
    update(job({ processed: 800, progress: 2 / 3 }));
    rerender(table());
    expect(
      screen.getByText("800 / 1200 work units processed")
    ).toBeInTheDocument();
    rerender(table());
    expect(
      screen.getByText("800 / 1200 work units processed")
    ).toBeInTheDocument();
    expect(
      screen.getByText("501 / 1200 work units processed")
    ).toBeInTheDocument();
    update(
      job({ id: "2", description: "Second import", processed: 600 }),
      GQL.JobStatusUpdateType.Add
    );
    rerender(table());
    expect(screen.getAllByText("Second import")).toHaveLength(1);
    expect(
      screen.getByText("501 / 1200 work units processed")
    ).toBeInTheDocument();
  });
});

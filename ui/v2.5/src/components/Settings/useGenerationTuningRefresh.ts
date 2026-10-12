import { useEffect, useRef, useState } from "react";
import type { ApolloQueryResult } from "@apollo/client";
import * as GQL from "src/core/generated-graphql";
import { getWSClient } from "src/core/StashService";

const REFRESH_DELAY = 5000;
const RETRY_DELAYS = [5000, 10000];

// Coalesce the existing triggers. Retain invalidations until a successful HTTP
// snapshot confirms them, with two delayed retries rather than ongoing polling.
export function useGenerationTuningRefresh(
  refetch: () => Promise<ApolloQueryResult<GQL.ConfigurationQuery>>,
  loading: boolean
) {
  const [stale, setStale] = useState(false);
  const latestRefetch = useRef(refetch);
  latestRefetch.current = refetch;
  const requestRefresh = useRef(() => {});
  const resumeRefresh = useRef(() => {});
  const queryLoading = useRef(loading);
  queryLoading.current = loading;
  const { data } = GQL.useJobsSubscribeSubscription();

  useEffect(() => {
    let timer: ReturnType<typeof setTimeout> | undefined;
    let pending = false;
    let dirty = false;
    let disposed = false;
    let revision = 0;
    let failures = 0;
    const delay = () => RETRY_DELAYS[failures - 1] ?? REFRESH_DELAY;
    const schedule = () => {
      if (
        disposed ||
        !dirty ||
        failures > RETRY_DELAYS.length ||
        document.hidden ||
        queryLoading.current ||
        pending ||
        timer
      )
        return;
      timer = setTimeout(async () => {
        timer = undefined;
        if (disposed || document.hidden || queryLoading.current) return;
        const requestedRevision = revision;
        pending = true;
        try {
          const response = await latestRefetch.current();
          const tuning = response.data?.configuration.general.generationTuning;
          if (response.errors?.length || !tuning?.sprite || !tuning.preview)
            throw new Error("Generation tuning snapshot unavailable");
          if (disposed) return;
          failures = 0;
          if (revision === requestedRevision) {
            dirty = false;
            setStale(false);
          }
        } catch {
          if (!disposed) {
            failures++;
            setStale(true);
          }
        } finally {
          pending = false;
          if (!disposed && dirty) schedule();
        }
      }, delay());
    };
    const invalidate = () => {
      if (disposed) return;
      revision++;
      dirty = true;
      setStale(true);
      // Loading transitions and bursts cannot replenish a running retry budget.
      // After exhaustion, only a new external trigger starts another cycle.
      if (failures > RETRY_DELAYS.length) failures = 0;
      schedule();
    };
    requestRefresh.current = invalidate;
    resumeRefresh.current = schedule;
    const visible = () => {
      if (!document.hidden) invalidate();
    };
    document.addEventListener("visibilitychange", visible);
    const reconnect = getWSClient().on("connected", invalidate);
    return () => {
      disposed = true;
      clearTimeout(timer);
      requestRefresh.current = () => {};
      resumeRefresh.current = () => {};
      document.removeEventListener("visibilitychange", visible);
      reconnect();
    };
  }, []);

  useEffect(() => {
    if (!loading) resumeRefresh.current();
  }, [loading]);

  useEffect(() => {
    const job = data?.jobsSubscribe.job;
    if (
      job &&
      (/^(Generating|Scanning)(?:\.{3}|\s)/.test(job.description) ||
        job.subTasks?.some((task) =>
          /^Generating (sprites|preview)\b/.test(task)
        ))
    ) {
      requestRefresh.current();
    }
  }, [data]);

  return stale;
}

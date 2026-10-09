import {
  faBan,
  faCheck,
  faCircleExclamation,
  faCog,
  faHourglassStart,
  faTimes,
  faMemory,
  faMicrochip,
  faFilm,
  faImage,
  faImages,
  faFolderOpen,
  faUser,
  faTag,
  faBuilding,
  faDownload,
  faUpload,
  faMagnifyingGlass,
  faWandMagicSparkles,
  faTrash,
  faBoxArchive,
} from "@fortawesome/free-solid-svg-icons";
import { formatRelativeTime } from "src/utils/date";
import React, { useEffect, useRef, useState } from "react";
import { useIntl } from "react-intl";
import { Icon } from "src/components/Shared/Icon";
import {
  getWSClient,
  mutateStopJob,
  useJobQueue,
  useJobsSubscribe,
} from "src/core/StashService";
import * as GQL from "src/core/generated-graphql";
import {
  Box,
  Paper,
  Typography,
  LinearProgress,
  IconButton,
  Chip,
  Tooltip,
  alpha,
  Theme,
} from "@mui/material";
import { appendActivityHistory, readActivityHistory } from "./jobActivity";
import { IconDefinition } from "@fortawesome/fontawesome-svg-core";

// ─── Job classification ──────────────────────────────────────────────────────

type MediaType = "scene" | "image" | "gallery" | "performer" | "tag" | "studio" | "generic";
type JobCategory = "scan" | "generate" | "identify" | "tag" | "export" | "import" | "download" | "clean" | "migrate" | "generic";

interface JobClass {
  media: MediaType;
  category: JobCategory;
  mediaIcon: IconDefinition;
  mediaColor: string;
  categoryIcon: IconDefinition;
  label: string;
}

function classifyJob(description: string): JobClass {
  const d = description.toLowerCase();

  let media: MediaType = "generic";
  let mediaIcon: IconDefinition = faCog;
  let mediaColor = "#6b7280";

  if (d.includes("scene")) { media = "scene"; mediaIcon = faFilm; mediaColor = "#3b82f6"; }
  else if (d.includes("gallery") || d.includes("galleries")) { media = "gallery"; mediaIcon = faFolderOpen; mediaColor = "#f59e0b"; }
  else if (d.includes("image")) { media = "image"; mediaIcon = faImage; mediaColor = "#8b5cf6"; }
  else if (d.includes("performer")) { media = "performer"; mediaIcon = faUser; mediaColor = "#ec4899"; }
  else if (d.includes("tag")) { media = "tag"; mediaIcon = faTag; mediaColor = "#10b981"; }
  else if (d.includes("studio")) { media = "studio"; mediaIcon = faBuilding; mediaColor = "#06b6d4"; }

  let category: JobCategory = "generic";
  let categoryIcon: IconDefinition = faCog;
  let label = "Task";

  if (d.includes("download")) { category = "download"; categoryIcon = faDownload; label = "Download"; }
  else if (d.includes("scan")) { category = "scan"; categoryIcon = faMagnifyingGlass; label = "Scan"; }
  else if (d.includes("generat") || d.includes("sprite") || d.includes("preview") || d.includes("thumbnail") || d.includes("phash")) { category = "generate"; categoryIcon = faImages; label = "Generate"; }
  else if (d.includes("identif")) { category = "identify"; categoryIcon = faWandMagicSparkles; label = "Identify"; }
  else if (d.includes("auto-tag") || d.includes("autotag")) { category = "tag"; categoryIcon = faTag; label = "Auto-tag"; }
  else if (d.includes("export")) { category = "export"; categoryIcon = faUpload; label = "Export"; }
  else if (d.includes("import")) { category = "import"; categoryIcon = faDownload; label = "Import"; }
  else if (d.includes("clean")) { category = "clean"; categoryIcon = faTrash; label = "Clean"; }
  else if (d.includes("migrat")) { category = "migrate"; categoryIcon = faBoxArchive; label = "Migrate"; }

  return { media, category, mediaIcon, mediaColor, categoryIcon, label };
}

// ─── Types ───────────────────────────────────────────────────────────────────

type JobFragment = Pick<
  GQL.Job,
  | "id"
  | "status"
  | "subTasks"
  | "description"
  | "progress"
  | "processed"
  | "total"
  | "error"
  | "startTime"
>;

interface IJob {
  job: JobFragment;
}

// ─── Task card ───────────────────────────────────────────────────────────────

const Task: React.FC<IJob> = ({ job }) => {
  const [stopping, setStopping] = useState(false);
  const [className, setClassName] = useState("");
  const [subTaskHistory, setSubTaskHistory] = useState<string[]>(() => {
    try {
      const stored = localStorage.getItem(`job-history-${job.id}`);
      return readActivityHistory(stored);
    } catch {
      return [];
    }
  });
  const terminalRef = useRef<HTMLDivElement>(null);
  const prevStatus = useRef(job.status);
  const [justFinished, setJustFinished] = useState(false);

  const jobClass = classifyJob(job.description);

  useEffect(() => {
    const frame = requestAnimationFrame(() => setClassName("fade-in"));
    return () => cancelAnimationFrame(frame);
  }, []);

  useEffect(() => {
    if (
      job.status === GQL.JobStatus.Cancelled ||
      job.status === GQL.JobStatus.Failed ||
      job.status === GQL.JobStatus.Finished
    ) {
      const timer = setTimeout(() => setClassName("fade-out"), 4500);
      return () => clearTimeout(timer);
    }
  }, [job.status]);

  useEffect(() => {
    if (prevStatus.current !== GQL.JobStatus.Finished && job.status === GQL.JobStatus.Finished) {
      setJustFinished(true);
      const timer = setTimeout(() => setJustFinished(false), 2000);
      return () => clearTimeout(timer);
    }
    prevStatus.current = job.status;
  }, [job.status]);

  useEffect(() => {
    if (job.subTasks && job.subTasks.length > 0) {
      setSubTaskHistory((prev) => appendActivityHistory(prev, job.subTasks!));
    }
  }, [job.subTasks]);

  useEffect(() => {
    try {
      if (subTaskHistory.length > 0) {
        localStorage.setItem(`job-history-${job.id}`, JSON.stringify(subTaskHistory));
      } else {
        localStorage.removeItem(`job-history-${job.id}`);
      }
    } catch (e) {
      console.error("Failed to save job history to localStorage", e);
    }
  }, [subTaskHistory, job.id]);

  useEffect(() => {
    if (terminalRef.current) {
      terminalRef.current.scrollTop = terminalRef.current.scrollHeight;
    }
  }, [subTaskHistory, job.error]);

  async function stopJob() {
    setStopping(true);
    await mutateStopJob(job.id);
  }

  function canStop() {
    return (
      !stopping &&
      (job.status === GQL.JobStatus.Ready || job.status === GQL.JobStatus.Running)
    );
  }

  const progress = (job.progress ?? 0) * 100;
  const isRunning = job.status === GQL.JobStatus.Running;
  const isFinished = job.status === GQL.JobStatus.Finished;
  const isFailed = job.status === GQL.JobStatus.Failed;
  const isCancelled = job.status === GQL.JobStatus.Cancelled;
  const isReady = job.status === GQL.JobStatus.Ready;

  const statusColor = isRunning
    ? "primary.main"
    : isFinished
    ? "success.main"
    : isFailed || isCancelled
    ? "error.main"
    : "warning.main";

  const getProgressColor = (): "primary" | "success" | "error" => {
    if (isFinished) return "success";
    if (isFailed) return "error";
    return "primary";
  };

  const getCardSx = () => {
    const base = {
      display: "flex",
      gap: 0,
      mb: 1.5,
      borderRadius: 1.5,
      background: "rgba(8,8,22,0.35)",
      backdropFilter: "blur(14px)",
      WebkitBackdropFilter: "blur(14px)",
      border: "1px solid",
      borderColor: "divider",
      overflow: "hidden",
      transition: "all 0.4s cubic-bezier(0.16, 1, 0.3, 1)",
      animation: "fadeIn 0.4s cubic-bezier(0.16, 1, 0.3, 1) forwards",
      "@keyframes fadeIn": {
        from: { opacity: 0, transform: "translateY(10px)" },
        to: { opacity: 1, transform: "translateY(0)" },
      },
      ...(className === "fade-out" && {
        animation: "fadeOut 0.4s cubic-bezier(0.16, 1, 0.3, 1) forwards",
        "@keyframes fadeOut": {
          from: { opacity: 1, transform: "translateY(0)" },
          to: { opacity: 0, transform: "translateY(-10px)" },
        },
      }),
    };

    if (isFinished) return {
      ...base,
      borderColor: (theme: Theme) => alpha(theme.palette.success.main, 0.25),
      ...(justFinished && {
        boxShadow: (theme: Theme) => `0 0 0 3px ${alpha(theme.palette.success.main, 0.2)}`,
      }),
    };
    if (isFailed) return {
      ...base,
      borderColor: (theme: Theme) => alpha(theme.palette.error.main, 0.25),
    };
    if (isCancelled) return { ...base, opacity: 0.65 };
    return base;
  };

  const exactWorkUnitCounts =
    Number.isSafeInteger(job.processed) &&
    (job.total == null || Number.isSafeInteger(job.total));
  const hasLog = subTaskHistory.length > 0 || !!job.error;

  return (
    <Paper variant="outlined" sx={getCardSx()}>
      {/* Left accent strip */}
      <Box
        sx={{
          width: 4,
          flexShrink: 0,
          bgcolor: isRunning
            ? "primary.main"
            : isFinished
            ? "success.main"
            : isFailed || isCancelled
            ? "error.main"
            : "warning.main",
          transition: "background-color 0.4s ease",
        }}
      />

      {/* Media type avatar */}
      <Box
        sx={{
          width: 64,
          flexShrink: 0,
          display: "flex",
          alignItems: "center",
          justifyContent: "center",
          bgcolor: alpha(jobClass.mediaColor, 0.08),
          borderRight: "1px solid",
          borderColor: "divider",
          py: 2,
        }}
      >
        <Box
          sx={{
            width: 36,
            height: 36,
            borderRadius: 1,
            bgcolor: alpha(jobClass.mediaColor, 0.15),
            display: "flex",
            alignItems: "center",
            justifyContent: "center",
            color: jobClass.mediaColor,
            fontSize: "1rem",
          }}
        >
          <Icon icon={jobClass.mediaIcon} />
        </Box>
      </Box>

      {/* Main content */}
      <Box sx={{ flex: 1, minWidth: 0, p: 1.75 }}>
        {/* Header row */}
        <Box sx={{ display: "flex", alignItems: "center", gap: 1, mb: 1.25 }}>
          <Chip
            icon={<Icon icon={jobClass.categoryIcon} />}
            label={jobClass.label}
            size="small"
            sx={{
              height: 20,
              fontSize: "0.7rem",
              fontWeight: 600,
              bgcolor: alpha(jobClass.mediaColor, 0.12),
              color: jobClass.mediaColor,
              border: "1px solid",
              borderColor: alpha(jobClass.mediaColor, 0.25),
              "& .MuiChip-icon": { fontSize: "0.65rem", color: jobClass.mediaColor },
            }}
          />
          {isFinished && (
            <Chip
              icon={<Icon icon={faCheck} />}
              label="Done"
              color="success"
              size="small"
              sx={{ height: 20, fontSize: "0.7rem" }}
            />
          )}
          {isCancelled && (
            <Chip icon={<Icon icon={faBan} />} label="Cancelled" size="small" sx={{ height: 20, fontSize: "0.7rem", opacity: 0.8 }} />
          )}
          {isFailed && (
            <Chip icon={<Icon icon={faCircleExclamation} />} label="Failed" color="error" size="small" sx={{ height: 20, fontSize: "0.7rem" }} />
          )}
          {isReady && (
            <Chip icon={<Icon icon={faHourglassStart} />} label="Queued" size="small" sx={{ height: 20, fontSize: "0.7rem", color: "warning.main", borderColor: "warning.main" }} variant="outlined" />
          )}

          <Box sx={{ flex: 1 }} />

          {/* Timestamp */}
          {job.startTime && (
            <Typography variant="caption" sx={{ color: "text.disabled", whiteSpace: "nowrap", fontSize: "0.7rem" }}>
              {formatRelativeTime(job.startTime)}
            </Typography>
          )}

          {/* Stop button */}
          <Tooltip title="Stop job" arrow>
            <span>
              <IconButton
                size="small"
                onClick={stopJob}
                disabled={!canStop()}
                sx={{
                  p: 0.5,
                  color: "text.disabled",
                  "&:hover": { color: "error.main" },
                  "&.Mui-disabled": { opacity: 0.3 },
                }}
              >
                <Icon icon={faTimes} />
              </IconButton>
            </span>
          </Tooltip>
        </Box>

        {/* Description */}
        <Typography
          variant="body2"
          sx={{
            fontWeight: 500,
            color: "text.primary",
            mb: 1.25,
            lineHeight: 1.4,
            fontSize: "0.85rem",
          }}
          title={job.description}
        >
          {job.description}
        </Typography>

        {/* Progress */}
        <Box sx={{ display: "flex", alignItems: "center", gap: 1.5, mb: hasLog ? 1.5 : 0 }}>
          <LinearProgress
            variant={isRunning && job.progress === null ? "indeterminate" : "determinate"}
            value={isFinished ? 100 : progress}
            color={getProgressColor()}
            sx={{
              height: 4,
              borderRadius: 2,
              flexGrow: 1,
              bgcolor: "action.hover",
              "& .MuiLinearProgress-bar": {
                borderRadius: 2,
                transition: "transform 0.4s cubic-bezier(0.16, 1, 0.3, 1)",
              },
            }}
          />
          {isRunning && job.progress !== null && (
            <Typography variant="caption" sx={{ color: statusColor, whiteSpace: "nowrap", fontWeight: 600, fontSize: "0.7rem", minWidth: 32, textAlign: "right" }}>
              {Math.round(progress)}%
            </Typography>
          )}
        </Box>

        {job.processed != null && (
          <Tooltip title="Reported work units; units vary by job and may include skipped or failed work. The total may change during execution." arrow>
            <Typography variant="caption" sx={{ display: "block", color: "text.secondary", mb: hasLog ? 1 : 0 }}>
              {!exactWorkUnitCounts
                ? "Work-unit counts exceed exact display range"
                : job.total != null
                ? `${job.processed} / ${job.total} work units processed`
                : `${job.processed} work units processed (total unknown)`}
            </Typography>
          </Tooltip>
        )}

        {/* Activity log */}
        {hasLog && (
          <Box
            ref={terminalRef}
            sx={{
              maxHeight: 160,
              overflowY: "auto",
              borderRadius: 1,
              border: "1px solid",
              borderColor: "divider",
              bgcolor: "rgba(0,0,0,0.25)",
              "&::-webkit-scrollbar": { width: "4px" },
              "&::-webkit-scrollbar-thumb": {
                bgcolor: "divider",
                borderRadius: "2px",
                "&:hover": { bgcolor: "text.disabled" },
              },
            }}
          >
            {/* Table header */}
            <Box
              sx={{
                display: "grid",
                gridTemplateColumns: "1fr",
                gap: 0,
                px: 1.25,
                py: 0.5,
                borderBottom: "1px solid",
                borderColor: "divider",
                bgcolor: (theme) => alpha(theme.palette.action.hover, 0.5),
                position: "sticky",
                top: 0,
                zIndex: 1,
              }}
            >
              <Typography variant="caption" sx={{ color: "text.disabled", fontSize: "0.65rem", fontWeight: 700 }}>
                Recent activity (up to 500 sampled messages)
              </Typography>
            </Box>

            {/* Log rows */}
            {subTaskHistory.map((t, i) => {
              const isLatest = i === subTaskHistory.length - 1;
              const rowActive = isLatest && isRunning;

              return (
                <Box
                  key={i}
                  sx={{
                    display: "grid",
                    gridTemplateColumns: "1fr",
                    gap: 0,
                    px: 1.25,
                    py: 0.4,
                    alignItems: "center",
                    borderBottom: i < subTaskHistory.length - 1 ? "1px solid" : "none",
                    borderColor: "divider",
                    bgcolor: rowActive ? (theme: Theme) => alpha(theme.palette.primary.main, 0.04) : "transparent",
                    transition: "background-color 0.2s ease",
                  }}
                >
                  <Typography
                    variant="body2"
                    sx={{
                      fontSize: "0.775rem",
                      color: rowActive ? "text.primary" : "text.secondary",
                      fontWeight: rowActive ? 500 : 400,
                      lineHeight: 1.4,
                      fontFamily: "monospace",
                      wordBreak: "break-all",
                    }}
                  >
                    {t}
                  </Typography>
                </Box>
              );
            })}

            {/* Error row */}
            {job.error && (
              <Box
                sx={{
                  display: "grid",
                  gridTemplateColumns: "1fr",
                  gap: 0,
                  px: 1.25,
                  py: 0.4,
                  alignItems: "center",
                  bgcolor: (theme: Theme) => alpha(theme.palette.error.main, 0.06),
                  borderTop: "1px solid",
                  borderColor: (theme: Theme) => alpha(theme.palette.error.main, 0.2),
                }}
              >
                <Typography variant="body2" sx={{ fontSize: "0.775rem", color: "error.main", fontWeight: 600, lineHeight: 1.4, fontFamily: "monospace", wordBreak: "break-all" }}>
                  {job.error}
                </Typography>
              </Box>
            )}
          </Box>
        )}
      </Box>
    </Paper>
  );
};

// ─── Resource monitor ─────────────────────────────────────────────────────────

const ResourceMonitor: React.FC = () => {
  const { data } = GQL.useSystemStatsQuery({
    pollInterval: 2000,
    fetchPolicy: "network-only",
  });

  if (!data?.systemStats) return null;

  return (
    <Box sx={{ display: "flex", justifyContent: "flex-end", gap: 1, mb: 1 }}>
      <Tooltip title="Memory Usage" arrow>
        <Chip
          icon={<Icon icon={faMemory} />}
          label={`${Math.round(data.systemStats.memory)} MB`}
          size="small"
          variant="outlined"
          sx={{ fontSize: "0.7rem", height: 22 }}
        />
      </Tooltip>
      <Tooltip title="Active Goroutines" arrow>
        <Chip
          icon={<Icon icon={faMicrochip} />}
          label={`${data.systemStats.goroutines}`}
          size="small"
          variant="outlined"
          sx={{ fontSize: "0.7rem", height: 22 }}
        />
      </Tooltip>
    </Box>
  );
};

// Preserve jobs updated by the subscription after a query began. Counts may
// legitimately reset or totals grow, so freshness cannot be inferred from values.
function reconcileQueueSnapshot(
  snapshot: JobFragment[],
  current: JobFragment[],
  changed: ReadonlySet<string>,
  terminal: ReadonlyMap<string, ReturnType<typeof setTimeout>>
): JobFragment[] {
  const currentById = new Map(current.map((j) => [j.id, j]));
  const included = new Set<string>();
  const next: JobFragment[] = [];
  for (const job of snapshot) {
    const value = changed.has(job.id) || terminal.has(job.id)
      ? currentById.get(job.id)
      : job;
    // A REMOVE may already have expired its grace timer while the query waited.
    if (value) {
      next.push(value);
      included.add(value.id);
    }
  }
  for (const job of current) {
    if (!included.has(job.id) && (changed.has(job.id) || terminal.has(job.id))) {
      next.push(job);
    }
  }
  return next;
}

// ─── Job table ────────────────────────────────────────────────────────────────

export const JobTable: React.FC = () => {
  const intl = useIntl();
  const jobStatus = useJobQueue();
  const jobsSubscribe = useJobsSubscribe();
  const { refetch } = jobStatus;
  const removalTimers = useRef(new Map<string, ReturnType<typeof setTimeout>>());
  const initialSnapshotReceived = useRef(false);
  const initialChanges = useRef(new Set<string>());
  const pendingRefresh = useRef<Set<string> | undefined>();

  useEffect(() => {
    const timers = removalTimers.current;
    return () => {
      timers.forEach(clearTimeout);
      timers.clear();
    };
  }, []);

  const [queue, setQueue] = useState<JobFragment[]>(jobStatus.data?.jobQueue ?? []);

  useEffect(() => {
    if (!jobStatus.data || initialSnapshotReceived.current) return;
    initialSnapshotReceived.current = true;
    const changed = initialChanges.current;
    initialChanges.current = new Set<string>();
    setQueue((q) => reconcileQueueSnapshot(
      jobStatus.data!.jobQueue ?? [], q, changed, removalTimers.current
    ));
  }, [jobStatus.data]);

  useEffect(() => {
    let disposed = false;
    async function refreshQueue() {
      // Keep one query in flight so an older query cannot finish after a newer one.
      if (pendingRefresh.current) return;
      const changed = new Set<string>();
      pendingRefresh.current = changed;
      try {
        const response = await refetch();
        if (!disposed && response.data) {
          setQueue((q) => reconcileQueueSnapshot(
            response.data.jobQueue ?? [], q, changed, removalTimers.current
          ));
        }
      } catch {
        // Retain the last snapshot; the next poll or reconnect can recover.
      } finally {
        if (pendingRefresh.current === changed) pendingRefresh.current = undefined;
      }
    }
    const timer = setInterval(() => { void refreshQueue(); }, 5000);
    const dispose = getWSClient().on("connected", () => { void refreshQueue(); });
    return () => {
      disposed = true;
      clearInterval(timer);
      dispose();
    };
  }, [refetch]);

  useEffect(() => {
    if (jobStatus.loading || !jobStatus.data) return;
    try {
      const activeIds = new Set(
        [...queue, ...(jobStatus.data.jobQueue ?? [])].map((j) => j.id)
      );
      for (let i = 0; i < localStorage.length; i++) {
        const key = localStorage.key(i);
        if (key && key.startsWith("job-history-")) {
          const jobId = key.substring("job-history-".length);
          if (!activeIds.has(jobId)) {
            localStorage.removeItem(key);
            i--;
          }
        }
      }
    } catch (e) {
      console.error("Failed to clean up stale job histories from localStorage", e);
    }
  }, [queue, jobStatus.loading, jobStatus.data]);

  useEffect(() => {
    if (!jobsSubscribe.data) return;

    const event = jobsSubscribe.data.jobsSubscribe;
    pendingRefresh.current?.add(event.job.id);
    if (!initialSnapshotReceived.current) initialChanges.current.add(event.job.id);

    function updateJob() {
      setQueue((q) =>
        q.some((j) => j.id === event.job.id)
          ? q.map((j) => {
              if (j.id !== event.job.id) return j;
              // REMOVE and UPDATE use separate server buffers. A pending running
              // update must not overwrite final counts or terminal status.
              if (
                removalTimers.current.has(j.id) &&
                event.type !== GQL.JobStatusUpdateType.Remove
              ) return j;
              return event.job;
            })
          : q.concat([event.job])
      );
    }

    switch (event.type) {
      case GQL.JobStatusUpdateType.Add:
        // ADD can arrive after an UPDATE from a different subscription buffer.
        setQueue((q) =>
          q.some((j) => j.id === event.job.id) ? q : q.concat([event.job])
        );
        break;
      case GQL.JobStatusUpdateType.Remove:
        updateJob();
        clearTimeout(removalTimers.current.get(event.job.id));
        removalTimers.current.set(
          event.job.id,
          setTimeout(() => {
            removalTimers.current.delete(event.job.id);
            setQueue((q) => q.filter((j) => j.id !== event.job.id));
          }, 5000)
        );
        break;
      case GQL.JobStatusUpdateType.Update:
        updateJob();
        break;
    }
  }, [jobsSubscribe.data]);

  if (!queue?.length) {
    return (
      <Box sx={{ display: "flex", flexDirection: "column", gap: 1.5 }}>
        <ResourceMonitor />
        <Box
          sx={{
            p: 5,
            textAlign: "center",
            border: "1px dashed",
            borderColor: "divider",
            borderRadius: 1.5,
            background: "rgba(8,8,22,0.35)",
            backdropFilter: "blur(14px)",
            WebkitBackdropFilter: "blur(14px)",
            display: "flex",
            flexDirection: "column",
            alignItems: "center",
            gap: 1.5,
            animation: "fadeIn 0.4s cubic-bezier(0.16, 1, 0.3, 1) forwards",
            "@keyframes fadeIn": {
              from: { opacity: 0, transform: "translateY(6px)" },
              to: { opacity: 1, transform: "translateY(0)" },
            },
          }}
        >
          <Box
            sx={{
              width: 40,
              height: 40,
              borderRadius: "50%",
              bgcolor: (theme) => alpha(theme.palette.success.main, 0.1),
              color: "success.main",
              display: "flex",
              alignItems: "center",
              justifyContent: "center",
              fontSize: "1rem",
            }}
          >
            <Icon icon={faCheck} />
          </Box>
          <Typography variant="subtitle2" sx={{ fontWeight: 600, color: "text.primary" }}>
            All tasks completed
          </Typography>
          <Typography variant="body2" sx={{ color: "text.secondary", maxWidth: 280, mx: "auto" }}>
            {intl.formatMessage({ id: "config.tasks.empty_queue" })}
          </Typography>
        </Box>
      </Box>
    );
  }

  return (
    <Box sx={{ display: "flex", flexDirection: "column", gap: 0 }}>
      <ResourceMonitor />
      {queue.map((j) => (
        <Task job={j} key={j.id} />
      ))}
    </Box>
  );
};

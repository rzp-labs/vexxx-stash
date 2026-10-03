# Generation configuration lifecycle

This configuration layer defines persisted generation requests and a frozen startup snapshot. Workload integration and the Settings UI are introduced in later increments of the candidate stack. Existing configurations retain software backends and legacy scheduling.

```yaml
generation:
  markers:
    backend: software # software, vaapi; stored qsv marker requests use CPU fallback
  sprites:
    backend: software # software, vaapi, qsv
  device: /dev/dri/renderD128
  budget:
    enabled: false
    processes: 1
    gpu_processes: 1
    threads: 1
```

Explicit Intel selection always enables the shared budget, even when `enabled` is false. Missing, zero and `auto` numeric values mean one. Limits must be integers in 0..64, and GPU process count cannot exceed total process count. The device must be a literal `/dev/dri/renderD<number>` path; capability probes check its actual availability and permissions. Invalid configuration fails validation. If a caller bypasses validation, generation conservatively uses software with a one-process budget and an actionable warning.

The configuration is snapshotted once per process/configuration lifetime. A validated complete or partial proposal is saved atomically and never replaces a running scheduler. Failed persistence retains the previous configuration; changed settings require an authorized restart.

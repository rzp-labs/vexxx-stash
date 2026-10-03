# Bounded media generation benchmark (VEX-1)

`scripts/gpu_generation/benchmark.py` is a Python standard-library command runner for serial, explicit media-generation manifests. It creates JSON and Markdown evidence without installing software, changing networking, accessing a tracker, or requiring a GPU. The runner does **not** sandbox the supplied command: use reviewed commands and synthetic fixtures only. Do not supply credentials, URLs, private media, background daemons, or commands that change host/container configuration.

CT102 is a functional Intel lab. Its 2-CPU/2-GiB limits and current integrated GPU do not establish B580 throughput, quality or CPU-headroom acceptance. Budgets remain unagreed until representative measurements and the user's acceptance targets are available.

## Invocation

Transfer the script and a reviewed manifest to CT102 through the authorized `prox-svc` / `pct` workflow. Root coordinates all remote execution. Use a fresh directory under the existing lab artifact area, leaving old evidence intact. Do not enable CT102 networking.

```sh
python3 benchmark.py manifest.json --cwd /var/lib/qsv-lab/vex-run-UNIQUE/work --output-dir /var/lib/qsv-lab/vex-run-UNIQUE/report
```

Create the fresh `work` directory separately before the run. The harness refuses to replace an existing report directory; output commands should use FFmpeg `-n`. Every invocation is serial (one command at a time), with at most 32 jobs. Each generation/probe/validation child has a 55-second maximum and a 3-second termination grace; manifests can request shorter limits. Validation is a separate serial child and separately bounded. No sustained load or concurrency testing is implied. Start with one short fixture and inspect resource evidence before scheduling more work. Repeat with separately named cases/runs rather than hiding repetition in shell commands.

The example `ct102-baseline.example.json` uses the existing `qsv-h264.mp4` synthetic fixture. It probes the source, runs a short CPU MP4 baseline and a lossless/compression-6 WebP baseline. Its commands demonstrate the harness and **do not represent the application's canonical quality settings or acceptance thresholds**. The current candidate's exact commands and revision must replace those examples before product comparisons. Known authorized fixture names also include `vaapi-h264.mp4`, `qsv-hevc.mp4` and `vaapi-hevc.mp4`; verify availability and metadata rather than creating replacements.

The example's MP4 decode validation verifies the decoder exits successfully. WebP validation remains untested because FFmpeg builds vary in animated WebP decoding support. Supply a reviewed validator for that build; do not claim successful exit proves animation/frame count or browser playback.

## Manifest and result schema

The machine-readable manifest schema is `scripts/gpu_generation/manifest.schema.json`; runtime validation additionally rejects duplicate case IDs and NUL bytes. The manifest is JSON with `schema_version: 1`, `candidate_revision` (exact 40-character Git SHA), `environment` (`ct102`, `b580` or `local-cpu`), `fixtures` and `cases`. Every fixture must declare `source_kind: synthetic` and a readable `path`; fixture bytes are hashed before execution. The harness cannot verify synthetic provenance from file contents: the declaration must reference the approved fixture source. The embedded manifest plus source digests make the result reproducible. Benchmark an exact immutable candidate, or record a patch hash separately and label an uncommitted working tree explicitly in the accompanying evidence.

Optional `environment_commands` collects up to eight named, reviewed metadata commands before workload cases. Each entry contains unique `name`, literal `argv`, optional safe `env`, and optional `timeout_seconds` defaulting to 3 seconds (maximum 3, plus cleanup grace). Use harmless version/kernel reads such as `/usr/bin/ffmpeg -version`, `/usr/bin/ffprobe -version`, `/usr/bin/uname -a`, and `/usr/bin/python3 --version`. Each full result, exact argv/environment and status is retained in the report's `environment_commands`; Markdown shows a first-line preview. Missing tools or other metadata failures are recorded without failing or skipping workload cases. Cancellation still cleans up and stops the run. Critical environment requirements should also be explicit normal cases if their failure must fail the CLI. This collection does not probe GPU capabilities or access secret configs.

Each case has a unique filename-safe `id`, `workload`, `selected_backend` (`cpu`, `qsv`, `vaapi`), `argv` (a literal argument list, never a shell string), optional `timeout_seconds`, optional `env`, optional `actual_backend`, optional `fallback_reason`, optional `validation` (another command spec), optional `lab_budget`, and optional `gpu_evidence`. Runtime environment is deliberately limited to `PATH`, `LANG`, `LC_ALL`, `TZ`, `TMPDIR`, `LIBVA_DRIVER_NAME`, `LIBVA_DRIVERS_PATH`, `LD_LIBRARY_PATH` and `OMP_NUM_THREADS`. The exact child environment is captured for every command; unspecified parent environment keys are not inherited.

The candidate driver prints a trailing JSON record with `diagnostics: [{"Selected":"vaapi","Actual":"vaapi","Stage":"","Reason":""}]` and `output_validation: {"status":"passed"}`. The harness derives actual backend from those runtime diagnostics (`software` normalizes to `cpu`) and stores the complete record. Manifest `actual_backend` and `fallback_reason` are expectations only. Plain FFmpeg commands have unknown actual backend unless the candidate emits runtime diagnostics, even when argv requests acceleration. For a mixed queue, top-level `actual_backend: per_job` (or a `per_job` summary diagnostic) takes precedence over every individual diagnostic. The report retains per-job attribution and explicit fallback summaries in `per_job_backend_evidence` / `per_job_fallbacks`, as well as the original runtime record. Aggregate GPU status remains `unverified` even with aligned positive video-engine samples: raw samples are retained for manual correlation to each job, and the final CPU/GPU subjob cannot label the whole queue. Driver output-validation failure remains a failure; successful process exit alone is insufficient.

A GPU evidence record references a separately collected host `intel_gpu_top` sampling file. It includes `case_id`, `monitor_start_unix` and `monitor_end_unix` in UTC Unix seconds. Inline evidence requires the file to change during the command and have contemporaneous mtime, with the capture covering the command runtime. Typical host orchestration transfers samples after the command, so prefer the attachment workflow below.

```sh
python3 benchmark.py --attach-results /var/lib/qsv-lab/vex-run-UNIQUE/report/results.json --gpu-evidence /var/lib/qsv-lab/vex-run-UNIQUE/host-evidence.json --output-dir /var/lib/qsv-lab/vex-run-UNIQUE/report-with-gpu
```

Attachment creates a new report directory without rerunning a job or overwriting the original report. The mapping JSON is keyed by the original case id, with exact candidate revision and command start time copied from the original result:

```json
{
  "marker-vaapi-r1": {
    "path": "/var/lib/qsv-lab/vex-run-UNIQUE/intel-gpu-top.json",
    "case_id": "marker-vaapi-r1",
    "candidate_revision": "639e67c3879f7eaae156b6edae60b456afa58d50",
    "command_started_unix": 1790899200.123,
    "monitor_start_unix": 1790899199.0,
    "monitor_end_unix": 1790899205.0
  }
}
```

Those timestamps are illustrative. Use recorded host capture times, never copied example values. Verify synchronized host/container clocks and correlate positive samples to the command interval; the runner validates envelope coverage, not per-sample ownership. Host capture metadata is an external evidence assertion and must be accurate. Keep one GPU job at a time and inspect other host activity to avoid attributing unrelated work to the case. Transfer mtime is deliberately not treated as capture time.

The harness parses standard Intel GPU JSON arrays or concatenated JSON objects, counting numeric `Video*` engine `busy` values; render activity alone is insufficient. `observed_video_activity` requires a successful command, runtime QSV/VAAPI actual backend, no software fallback, aligned capture metadata and positive video-engine samples. The original sample file is hashed in full and attached. A bounded monitor stop can leave an incomplete terminal object or missing closing array bracket: only complete prefix samples are interpreted, with `parse_status: complete_prefix_with_unparsed_tail`, `truncated_tail_bytes` and `parse_error` recorded explicitly. Array delimiters are checked: duplicate/trailing commas or a missing separator stop parsing at the complete prefix and report the remaining tail. An unparsed tail can also be malformed; the parser does not repair it or infer counters from it. Zero complete samples fail parsing. Inspect these diagnostics when judging sample coverage; retained prefix samples do not prove full capture coverage. A `confirmed_activity` boolean is ignored. Unknown actual backend remains unverified; CPU/fallback cases never receive GPU credit even if other work generated positive samples. This is evidence of observed activity, not proof that every FFmpeg stage ran on GPU. Separate decode/filter/encode probes and runtime diagnostics establish supported boundaries.

`results.json` contains the full manifest, fixture identities, command argument lists/environment, command status/exit code, separate validation result/status, wall seconds, direct-child `wait4` user/system/total CPU seconds, peak RSS bytes, optional `/proc` process-group samples and peak/steady sampled CPU/RSS, backend/fallback evidence and lab budget. `report.md` provides a concise comparison table. Statuses are `completed`, `failed`, `launch_failed`, `timed_out`, `cancelled` or `output_limit`; validation without a validator is `untested`. Stdout and stderr are capped at 64 KiB each; reaching the cap terminates the process group. A nonzero generation or validation status makes the CLI exit nonzero after writing the report.

Peak RSS from `wait4` is the child's maximum resident set, not a sum of descendant maxima. `/proc` RSS samples sum the group at each sampling instant and can double-count shared pages. CPU samples use one CPU = 100%, so two CPUs can reach 200%. Steady measurements are the central half of sampled intervals; short jobs may have insufficient samples. Exited group members' CPU counters can disappear; negative intervals are discarded. `wait4` includes descendants only if the child reaps them, so arbitrary daemon trees must not be benchmarked with this runner. Pipe holders are cleaned as a process group; SIGINT/SIGTERM cancellation also kills/reaps its direct child and stops subsequent cases. SIGKILL, machine loss or uninterruptible kernel waits cannot be cleaned up by a Python signal handler; the authorized outer orchestrator must verify no remaining process-group work in those cases. This is a bounded lab runner, not an operating-system resource sandbox.

Responsiveness is always `untested` in this harness: it does not fabricate a service latency measurement from a transcode. Collect timestamped request latencies from an authorized test instance in a separate artifact, with idle/control and workload intervals, before accepting CPU-headroom or mixed-queue responsiveness claims. Capture CPU configuration, cgroup CPU/RAM/swap limits, runtime/FFmpeg versions, device/driver, host-idle evidence and fixture/candidate hashes in the run notes. Do not read secret configs.

## Focused tests

```sh
python3 -m unittest discover -s scripts/gpu_generation -p 'test_*.py' -v
```

Tests exercise real child success/failure/launch failure, timed-out descendant cleanup, SIGTERM-resistant child kill after grace, external SIGTERM cancellation/reaping, bounded output, environment/timeout validation, runtime backend/validation diagnostics, stale/unaligned GPU evidence rejection, concatenated/array GPU JSON parsing with partial terminal-record diagnostics and wholly malformed rejection, fallback/GPU evidence distinctions, separate output-validation failure and refusal to overwrite evidence. They run without media, networking or a render device. Linux `/proc` metrics and hardware evidence require a separately authorized CT102 run.

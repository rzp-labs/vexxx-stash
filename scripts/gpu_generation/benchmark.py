#!/usr/bin/env python3
"""Serial, bounded Linux media benchmark. Standard library; no shell/network setup."""
import argparse
import hashlib
import json
import math
import os
from pathlib import Path
import re
import selectors
import signal
import subprocess
import time

SCHEMA_VERSION = 1
MAX_TIMEOUT = 55.0
KILL_GRACE = 3.0
OUTPUT_LIMIT = 65536
ENV_KEYS = {"PATH", "LANG", "LC_ALL", "TZ", "TMPDIR", "LIBVA_DRIVER_NAME",
            "LIBVA_DRIVERS_PATH", "LD_LIBRARY_PATH", "OMP_NUM_THREADS"}


def finite_number(value, lower, upper, field):
    if isinstance(value, bool) or not isinstance(value, (int, float)) or not math.isfinite(value) or not lower <= value <= upper:
        raise ValueError(f"{field} must be between {lower} and {upper}")
    return float(value)


def command_spec(spec):
    argv = spec.get("argv")
    if not isinstance(argv, list) or not 1 <= len(argv) <= 256 or any(not isinstance(a, str) or not a or "\0" in a or len(a) > 8192 for a in argv):
        raise ValueError("argv must contain 1–256 nonempty strings")
    timeout = finite_number(spec.get("timeout_seconds", MAX_TIMEOUT), 0.05, MAX_TIMEOUT, "timeout_seconds")
    env = {key: os.environ[key] for key in ENV_KEYS if key in os.environ}
    overrides = spec.get("env", {})
    if not isinstance(overrides, dict) or set(overrides) - ENV_KEYS or any(not isinstance(v, str) or "\0" in v for v in overrides.values()):
        raise ValueError("env accepts only documented runtime keys with string values")
    env.update(overrides)
    return argv, timeout, env


def validate_manifest(manifest):
    if manifest.get("schema_version") != SCHEMA_VERSION:
        raise ValueError("unsupported schema_version")
    if not re.fullmatch(r"[0-9a-f]{40}", manifest.get("candidate_revision", "")):
        raise ValueError("candidate_revision must be an exact 40-character Git SHA")
    if manifest.get("environment") not in ("ct102", "b580", "local-cpu"):
        raise ValueError("environment must be ct102, b580, or local-cpu")
    metadata_commands = manifest.get("environment_commands", [])
    if not isinstance(metadata_commands, list) or len(metadata_commands) > 8:
        raise ValueError("environment_commands must contain at most 8 bounded metadata commands")
    metadata_names = set()
    for command in metadata_commands:
        if not isinstance(command, dict):
            raise ValueError("environment command must be an object")
        name = command.get("name", "")
        if not isinstance(name, str) or not re.fullmatch(r"[a-zA-Z0-9_-]{1,80}", name) or name in metadata_names:
            raise ValueError("environment command name must be unique and filename-safe")
        metadata_names.add(name)
        command_spec(dict(command, timeout_seconds=command.get("timeout_seconds", 3)))
        finite_number(command.get("timeout_seconds", 3), 0.05, 3, "environment command timeout_seconds")
    fixtures = manifest.get("fixtures")
    if not isinstance(fixtures, list) or not fixtures:
        raise ValueError("declare authorized synthetic fixtures")
    for fixture in fixtures:
        if not isinstance(fixture, dict) or fixture.get("source_kind") != "synthetic" or not isinstance(fixture.get("path"), str) or not fixture["path"]:
            raise ValueError("only declared synthetic fixtures are allowed")
    cases = manifest.get("cases")
    if not isinstance(cases, list) or not 1 <= len(cases) <= 32:
        raise ValueError("cases must contain 1–32 serial jobs")
    ids = set()
    for case in cases:
        if not isinstance(case, dict):
            raise ValueError("case must be an object")
        cid = case.get("id", "")
        if not isinstance(cid, str) or not re.fullmatch(r"[a-zA-Z0-9_-]{1,80}", cid) or cid in ids:
            raise ValueError("case id must be unique and filename-safe")
        ids.add(cid)
        command_spec(case)
        if "validation" in case:
            command_spec(case["validation"])
        if case.get("selected_backend") not in ("cpu", "qsv", "vaapi"):
            raise ValueError("case selected_backend must be cpu, qsv, or vaapi")
        # Actual backend is evidence, never inferred from selected backend.
        if case.get("actual_backend") not in (None, "cpu", "qsv", "vaapi", "unknown"):
            raise ValueError("invalid actual_backend")
        if "fallback_reason" in case and not isinstance(case["fallback_reason"], str):
            raise ValueError("fallback_reason must be text")
        if "gpu_evidence" in case:
            gpu = case["gpu_evidence"]
            if not isinstance(gpu, dict) or not isinstance(gpu.get("path"), str) or not gpu["path"]:
                raise ValueError("gpu_evidence needs a nonempty file path")


def proc_sample(group):
    """Aggregate a private command's process group; Linux /proc is optional."""
    try:
        ticks = os.sysconf("SC_CLK_TCK")
        page = os.sysconf("SC_PAGE_SIZE")
        cpu_ticks = rss_pages = count = 0
        for entry in Path("/proc").iterdir():
            if not entry.name.isdigit():
                continue
            try:
                raw = (entry / "stat").read_text()
                fields = raw[raw.rfind(")") + 2:].split()
                if int(fields[2]) == group:
                    cpu_ticks += int(fields[11]) + int(fields[12])
                    rss_pages += max(0, int(fields[21]))
                    count += 1
            except (OSError, ValueError, IndexError):
                continue
        if not count:
            return None
        return {"cpu_seconds": cpu_ticks / ticks, "rss_bytes": rss_pages * page, "process_count": count}
    except (OSError, ValueError):
        return None


def kill_group(group, sig):
    try:
        os.killpg(group, sig)
    except ProcessLookupError:
        pass


def run_command(spec, cwd):
    # The child owns a separate session. Default SIGTERM would abandon it.
    previous = {sig: signal.getsignal(sig) for sig in (signal.SIGTERM, signal.SIGINT)}
    cancelled = False
    def terminate(_signum, _frame):
        # Defer cancellation until Popen has returned child ownership and the
        # cleanup scope exists. Do not mask signals in the inherited child.
        nonlocal cancelled
        cancelled = True
    for sig in previous:
        signal.signal(sig, terminate)
    try:
        return _run_command(spec, cwd, lambda: cancelled)
    finally:
        for sig, handler in previous.items():
            signal.signal(sig, handler)


def _run_command(spec, cwd, cancelled=lambda: False):
    argv, timeout, env = command_spec(spec)
    started = time.monotonic()
    started_unix = time.time()
    buffers = {"stdout": bytearray(), "stderr": bytearray()}
    selector = None
    status, usage, exit_code = "completed", None, None
    stop_at = None
    samples = []
    next_sample = started
    cleanup_killed = False
    try:
        proc = subprocess.Popen(argv, cwd=cwd, env=env, stdin=subprocess.DEVNULL,
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
    except OSError as exc:
        return {"argv": argv, "environment": env, "status": "launch_failed", "error": str(exc),
                "wall_seconds": time.monotonic() - started, "started_unix": started_unix, "ended_unix": time.time(), "exit_code": None, "samples": []}
    try:
        if cancelled():
            raise KeyboardInterrupt("benchmark terminated")
        # Establish child cleanup before selector/pipe setup, which can also be
        # interrupted or fail after the child has started its private session.
        selector = selectors.DefaultSelector()
        for name, pipe in (("stdout", proc.stdout), ("stderr", proc.stderr)):
            os.set_blocking(pipe.fileno(), False)
            selector.register(pipe, selectors.EVENT_READ, name)
        while exit_code is None or selector.get_map():
            if cancelled():
                raise KeyboardInterrupt("benchmark terminated")
            now = time.monotonic()
            if now >= next_sample and exit_code is None:
                sample = proc_sample(proc.pid)
                if sample:
                    sample["elapsed_seconds"] = now - started
                    samples.append(sample)
                next_sample = now + 0.1
            if stop_at is None and now - started >= timeout:
                status, stop_at = "timed_out", now
                kill_group(proc.pid, signal.SIGTERM)
            if stop_at is not None and now - stop_at >= KILL_GRACE:
                kill_group(proc.pid, signal.SIGKILL)
                cleanup_killed = True
            for key, _ in selector.select(0.05):
                chunk = os.read(key.fd, 8192)
                if not chunk:
                    selector.unregister(key.fileobj)
                    key.fileobj.close()
                    continue
                buffer = buffers[key.data]
                available = OUTPUT_LIMIT - len(buffer)
                buffer.extend(chunk[:max(0, available)])
                if len(chunk) > available and stop_at is None:
                    status, stop_at = "output_limit", time.monotonic()
                    kill_group(proc.pid, signal.SIGTERM)
            if exit_code is None:
                pid, wait_status, result_usage = os.wait4(proc.pid, os.WNOHANG)
                if pid:
                    exit_code = os.waitstatus_to_exitcode(wait_status)
                    usage = result_usage
                    proc.returncode = exit_code
                    # Clean surviving children, even when the leader exited successfully.
                    kill_group(proc.pid, signal.SIGTERM)
                    if stop_at is None:
                        stop_at = time.monotonic()
            if exit_code is not None and stop_at is not None and time.monotonic() - stop_at >= KILL_GRACE:
                kill_group(proc.pid, signal.SIGKILL)
                cleanup_killed = True
                # A descendant that escaped the group must not keep inherited pipes open.
                for key in list(selector.get_map().values()):
                    selector.unregister(key.fileobj)
                    key.fileobj.close()
                break
    except KeyboardInterrupt:
        status = "cancelled"
    finally:
        kill_group(proc.pid, signal.SIGKILL)
        if exit_code is None:
            _, wait_status, usage = os.wait4(proc.pid, 0)
            exit_code = os.waitstatus_to_exitcode(wait_status)
            proc.returncode = exit_code
        if selector is not None:
            selector.close()
        for pipe in (proc.stdout, proc.stderr):
            if not pipe.closed:
                pipe.close()
    if status == "completed" and exit_code != 0:
        status = "failed"
    cpu_percent = []
    for previous, sample in zip(samples, samples[1:]):
        delta = sample["elapsed_seconds"] - previous["elapsed_seconds"]
        # Exited subprocess counters can disappear; discard negative intervals.
        cpu_delta = sample["cpu_seconds"] - previous["cpu_seconds"]
        if delta > 0 and cpu_delta >= 0:
            cpu_percent.append(100 * cpu_delta / delta)
    # "Steady" means central half of available sample intervals, no warmup assertion.
    steady = cpu_percent[len(cpu_percent)//4: max(len(cpu_percent)//4 + 1, len(cpu_percent)*3//4)]
    return {"argv": argv, "environment": env, "status": status, "exit_code": exit_code,
            "timeout_seconds": timeout, "wall_seconds": time.monotonic() - started,
            "started_unix": started_unix, "ended_unix": time.time(),
            "cpu_user_seconds": usage.ru_utime, "cpu_system_seconds": usage.ru_stime,
            "cpu_seconds": usage.ru_utime + usage.ru_stime,
            "peak_rss_bytes": usage.ru_maxrss if os.uname().sysname == "Darwin" else usage.ru_maxrss * 1024,
            "sampled_peak_group_rss_bytes": max((s["rss_bytes"] for s in samples), default=None),
            "sampled_peak_cpu_percent": max(cpu_percent, default=None),
            "sampled_steady_cpu_percent": sum(steady) / len(steady) if steady else None,
            "sampled_steady_group_rss_bytes": (sum(s["rss_bytes"] for s in samples[len(samples)//4: max(len(samples)//4+1, len(samples)*3//4)]) / len(samples[len(samples)//4: max(len(samples)//4+1, len(samples)*3//4)])) if samples else None,
            "sampling_status": "sampled" if samples else "unavailable", "samples": samples,
            "cleanup_sigkill_sent": cleanup_killed,
            "stdout": buffers["stdout"].decode("utf-8", errors="replace"),
            "stderr": buffers["stderr"].decode("utf-8", errors="replace")}


def evidence_file(path):
    source = Path(path)
    if not source.is_file():
        raise ValueError(f"evidence file missing: {path}")
    digest = hashlib.sha256()
    with source.open("rb") as stream:
        for chunk in iter(lambda: stream.read(65536), b""):
            digest.update(chunk)
    return {"path": str(source.resolve()), "sha256": digest.hexdigest(), "bytes": source.stat().st_size}


def runtime_record(execution):
    """Only the candidate driver's trailing JSON record supplies actual backend."""
    lines = execution.get("stdout", "").strip().splitlines()
    if not lines:
        return None
    try:
        record = json.loads(lines[-1])
    except (ValueError, TypeError):
        return None
    if not isinstance(record, dict) or not isinstance(record.get("diagnostics"), list):
        return None
    return record


def runtime_backend(record):
    if not record:
        return "unknown", None
    if record.get("actual_backend") == "per_job" or any(
            isinstance(d, dict) and d.get("Actual") == "per_job" for d in record["diagnostics"]):
        # A mixed queue has no single backend; never inherit its final subjob.
        return "per_job", None
    actual, fallback = "unknown", None
    for diagnostic in record["diagnostics"]:
        if not isinstance(diagnostic, dict):
            continue
        value = diagnostic.get("Actual")
        value = "cpu" if value == "software" else value
        selected = diagnostic.get("Selected")
        if value in ("cpu", "qsv", "vaapi"):
            actual = value
            if selected in ("qsv", "vaapi") and value == "cpu":
                fallback = diagnostic.get("Reason") or "runtime reported software fallback"
    return actual, fallback


def runtime_jobs(record):
    if not record or not isinstance(record.get("output_validation"), dict):
        return []
    jobs = record["output_validation"].get("jobs")
    if not isinstance(jobs, list):
        return []
    attributed = []
    for job in jobs:
        if not isinstance(job, dict):
            continue
        diagnostics = job.get("diagnostics", [])
        if not isinstance(diagnostics, list):
            diagnostics = []
        actual, fallback = runtime_backend({"diagnostics": diagnostics})
        # Explicit job attribution is independent of aggregate diagnostic order.
        if job.get("actual_backend") in ("software", "cpu", "qsv", "vaapi"):
            actual = "cpu" if job["actual_backend"] == "software" else job["actual_backend"]
        attributed.append({"name": job.get("name"), "status": job.get("status"),
                           "selected_backend": job.get("selected_backend"),
                           "actual_backend": actual, "fallback_reason": fallback,
                           "diagnostics": diagnostics})
    return attributed


def gpu_activity(path):
    """Parse intel_gpu_top JSON (array or concatenated JSON objects)."""
    source = Path(path)
    if source.stat().st_size > 8 * 1024 * 1024:
        raise ValueError("GPU sample evidence exceeds 8 MiB")
    raw = source.read_text()
    decoder = json.JSONDecoder()
    records, index = [], 0
    while index < len(raw) and raw[index].isspace():
        index += 1
    array_mode = index < len(raw) and raw[index] == "["
    if array_mode:
        index += 1
    array_closed = not array_mode
    parse_status, parse_error, truncated_tail_bytes = "complete", None, 0
    while True:
        while index < len(raw) and raw[index].isspace():
            index += 1
        if index >= len(raw):
            if not array_closed:
                parse_status = "complete_prefix_with_unparsed_tail"
                parse_error = "GPU JSON array ended before its closing bracket"
            break
        if array_mode and raw[index] == "]":
            array_closed = True
            index += 1
            while index < len(raw) and raw[index].isspace():
                index += 1
            if index < len(raw):
                parse_status = "complete_prefix_with_unparsed_tail"
                parse_error = "Unexpected content after closing GPU JSON array"
                truncated_tail_bytes = len(raw[index:].encode("utf-8"))
            break
        if records:
            if raw[index] == ",":
                separator = index
                index += 1
                while index < len(raw) and raw[index].isspace():
                    index += 1
                if index >= len(raw) or raw[index] in ",]":
                    parse_status = "complete_prefix_with_unparsed_tail"
                    parse_error = "GPU JSON separator is not followed by a sample object"
                    truncated_tail_bytes = len(raw[separator:].encode("utf-8"))
                    break
            elif array_mode:
                parse_status = "complete_prefix_with_unparsed_tail"
                parse_error = "Expected comma or closing bracket after GPU JSON sample"
                truncated_tail_bytes = len(raw[index:].encode("utf-8"))
                break
        start = index
        try:
            record, index = decoder.raw_decode(raw, index)
        except json.JSONDecodeError as exc:
            if not records:
                raise ValueError(f"GPU evidence has no complete JSON samples: {exc}") from exc
            # Do not repair JSON or interpret counters from an unfinished sample.
            parse_status = "complete_prefix_with_unparsed_tail"
            parse_error = str(exc)
            truncated_tail_bytes = len(raw[start:].encode("utf-8"))
            break
        if not isinstance(record, dict):
            raise ValueError("GPU evidence samples must be JSON objects")
        records.append(record)
    if not records:
        raise ValueError("GPU evidence has no complete JSON samples")
    video_busy = []
    for record in records:
        if not isinstance(record, dict):
            continue
        engines = record.get("engines", {})
        if not isinstance(engines, dict):
            continue
        for name, engine in engines.items():
            # Render activity alone is not encode/decode evidence.
            if isinstance(name, str) and name.startswith("Video") and isinstance(engine, dict):
                busy = engine.get("busy")
                if isinstance(busy, (int, float)) and not isinstance(busy, bool) and math.isfinite(busy) and 0 <= busy <= 100:
                    video_busy.append(busy)
    return {"parse_status": parse_status, "parse_error": parse_error,
            "truncated_tail_bytes": truncated_tail_bytes,
            "sample_count": len(records), "video_busy_samples": len(video_busy),
            "peak_video_busy_percent": max(video_busy, default=None),
            "video_activity_observed": any(value > 0 for value in video_busy)}


def valid_timestamp(value):
    return isinstance(value, (int, float)) and not isinstance(value, bool) and math.isfinite(value) and value >= 0


def attach_gpu_evidence(report, mapping):
    """Enrich a copied result after the host monitor has stopped/transferred files."""
    known = {case["id"] for case in report["cases"]}
    if not isinstance(mapping, dict) or set(mapping) - known:
        raise ValueError("GPU evidence mapping has unknown case IDs")
    for case in report["cases"]:
        gpu = mapping.get(case["id"])
        if not gpu:
            continue
        if not isinstance(gpu, dict) or not isinstance(gpu.get("path"), str):
            raise ValueError("GPU evidence mapping entries need a file path")
        result = case["execution"]
        evidence = dict(gpu, **evidence_file(gpu["path"]), **gpu_activity(gpu["path"]))
        # Capture times refer to host sampling, not transfer time / local file mtime.
        aligned = (gpu.get("case_id") == case["id"] and
                   gpu.get("candidate_revision") == report["candidate_revision"] and
                   gpu.get("command_started_unix") == result["started_unix"] and
                   valid_timestamp(gpu.get("monitor_start_unix")) and
                   valid_timestamp(gpu.get("monitor_end_unix")) and
                   gpu["monitor_start_unix"] <= result["started_unix"] and
                   gpu["monitor_end_unix"] >= result["ended_unix"])
        evidence["monitor_covers_command"] = aligned
        evidence["timing_source"] = "external host capture metadata; transfer mtime is not evidence"
        case["gpu_evidence"] = evidence
        if case["actual_backend"] == "cpu" or case["fallback_reason"]:
            case["gpu_evidence_status"] = "cpu_fallback" if case["fallback_reason"] else "cpu"
        elif (result["status"] == "completed" and case["actual_backend"] in ("qsv", "vaapi") and
              aligned and evidence["video_activity_observed"]):
            case["gpu_evidence_status"] = "observed_video_activity"
        else:
            case["gpu_evidence_status"] = "unverified"
    return report


def render_markdown(report):
    lines = ["# Media generation benchmark", "", f"Candidate: `{report['candidate_revision']}`",
             f"Environment: `{report['environment']}`; serial job limit: 1.",
             "", "CT102 lab observations are separate from B580 acceptance. B580 budgets: unagreed.",
             "Responsiveness: untested (no service request latency is collected by this harness).", "",
             "| Case | Run status | Validation | CPU s | Wall s | Peak RSS MiB | Actual backend | GPU evidence |",
             "|---|---|---|---:|---:|---:|---|---|"]
    for case in report["cases"]:
        result = case["execution"]
        rss = result.get("peak_rss_bytes")
        lines.append(f"| {case['id']} | {result['status']} | {case['validation_status']} | {result.get('cpu_seconds', 0):.3f} | {result['wall_seconds']:.3f} | {rss/1048576:.1f}" if rss is not None else f"| {case['id']} | {result['status']} | {case['validation_status']} | — | {result['wall_seconds']:.3f} | —")
        lines[-1] += f" | {case['actual_backend']} | {case['gpu_evidence_status']} |"
    if report.get("environment_commands"):
        lines += ["", "## Environment metadata", "", "| Command | Status | Version / output preview |", "|---|---|---|"]
        for metadata in report["environment_commands"]:
            result = metadata["execution"]
            output = (result.get("stdout") or result.get("stderr") or result.get("error") or "").strip().splitlines()
            preview = output[0][:200] if output else "—"
            preview = preview.replace("|", "\\|").replace("<", "&lt;").replace(">", "&gt;")
            lines.append(f"| {metadata['name']} | {result['status']} | {preview} |")
    lines += ["", "CPU percentages use one CPU = 100%; a two-CPU job can exceed 100%.",
              "wait4 accounts for the direct child and descendants it reaps; /proc group samples are optional and may miss short peaks.",
              "Successful exit alone does not prove output validity, visual quality, playback compatibility, GPU activity, or responsiveness.", ""]
    return "\n".join(lines)


def benchmark(manifest, cwd):
    validate_manifest(manifest)
    started_at_utc = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    # Record fixture digests before running commands; do not alter existing fixtures.
    fixtures = [dict(fixture, **evidence_file(fixture["path"])) for fixture in manifest["fixtures"]]
    environment_results = []
    metadata_cancelled = False
    for command in manifest.get("environment_commands", []):
        result = run_command(dict(command, timeout_seconds=command.get("timeout_seconds", 3)), cwd)
        environment_results.append({"name": command["name"], "execution": result})
        if result["status"] == "cancelled":
            metadata_cancelled = True
            break
    cases = []
    for spec in ([] if metadata_cancelled else manifest["cases"]):
        gpu = spec.get("gpu_evidence")
        before_gpu = None
        if gpu and Path(gpu["path"]).is_file():
            before_gpu = evidence_file(gpu["path"])["sha256"]
        execution = run_command(spec, cwd)
        record = runtime_record(execution)
        validation = None
        if execution["status"] == "completed" and "validation" in spec:
            validation = run_command(spec["validation"], cwd)
        actual, fallback = runtime_backend(record)
        per_job = runtime_jobs(record)
        evidence = None
        gpu_status = "unverified"
        if gpu:
            try:
                evidence = dict(gpu, **evidence_file(gpu["path"]), **gpu_activity(gpu["path"]))
                modified = Path(gpu["path"]).stat().st_mtime
                fresh = (evidence["sha256"] != before_gpu and
                         execution["started_unix"] - 1 <= modified <= execution["ended_unix"] + 1)
                aligned = (gpu.get("case_id") == spec["id"] and
                           valid_timestamp(gpu.get("monitor_start_unix")) and
                           valid_timestamp(gpu.get("monitor_end_unix")) and
                           gpu["monitor_start_unix"] <= execution["started_unix"] and
                           gpu["monitor_end_unix"] >= execution["ended_unix"])
                evidence["fresh_during_command"] = fresh
                evidence["monitor_covers_command"] = aligned
                if execution["status"] == "completed" and actual in ("qsv", "vaapi") and not fallback and fresh and aligned and evidence["video_activity_observed"]:
                    gpu_status = "observed_video_activity"
            except (OSError, ValueError, TypeError) as exc:
                evidence = {"path": gpu.get("path"), "error": str(exc)}
        if actual == "cpu" or fallback:
            gpu_status = "cpu_fallback" if fallback else "cpu"
        driver_validation = record.get("output_validation") if record else None
        validation_status = validation["status"] if validation else "untested"
        if isinstance(driver_validation, dict):
            # Both sources remain in the report. A successful separate command
            # cannot override a driver failure or prove its untested checks.
            if driver_validation.get("status") in ("failed", "error"):
                validation_status = "failed"
            elif validation_status in ("completed", "untested"):
                if driver_validation.get("status") == "untested":
                    validation_status = "untested"
                elif validation is None and driver_validation.get("status") == "passed" and execution["status"] == "completed":
                    validation_status = "completed"
        cases.append({"id": spec["id"], "workload": spec.get("workload", "unspecified"),
                      "selected_backend": spec["selected_backend"], "actual_backend": actual,
                      "actual_backend_source": "candidate runtime diagnostics" if record else "unverified",
                      "expected_actual_backend": spec.get("actual_backend"),
                      "backend_expectation_matches": actual == spec["actual_backend"] if "actual_backend" in spec else None,
                      "runtime_record": record, "driver_output_validation": driver_validation,
                      "per_job_backend_evidence": per_job,
                      "per_job_fallbacks": [job for job in per_job if job["fallback_reason"]],
                      "fallback_reason": fallback, "expected_fallback_reason": spec.get("fallback_reason"), "execution": execution, "validation": validation,
                      "validation_status": validation_status,
                      "gpu_evidence": evidence, "gpu_evidence_status": gpu_status,
                      "budget": spec.get("lab_budget", "unagreed")})
        if execution["status"] == "cancelled" or (validation and validation["status"] == "cancelled"):
            break
    return {"schema_version": SCHEMA_VERSION, "candidate_revision": manifest["candidate_revision"],
            "environment": manifest["environment"], "started_at_utc": started_at_utc,
            "serial_job_limit": 1, "manifest": manifest, "fixtures": fixtures, "cases": cases,
            "environment_commands": environment_results, "environment_collection_cancelled": metadata_cancelled,
            "responsiveness": {"status": "untested"}, "b580_acceptance": {"status": "deferred", "budgets": "unagreed"}}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("manifest", type=Path, nargs="?")
    parser.add_argument("--attach-results", type=Path, help="existing results.json to enrich into a new report directory")
    parser.add_argument("--gpu-evidence", type=Path, help="case-id keyed JSON mapping of host capture evidence")
    parser.add_argument("--output-dir", type=Path, required=True, help="new directory; existing paths are never overwritten")
    parser.add_argument("--cwd", type=Path, default=Path.cwd())
    args = parser.parse_args()
    if args.attach_results:
        if args.manifest or not args.gpu_evidence:
            parser.error("--attach-results requires --gpu-evidence and no manifest")
        report = attach_gpu_evidence(json.loads(args.attach_results.read_text()), json.loads(args.gpu_evidence.read_text()))
        args.output_dir.mkdir(parents=True, exist_ok=False)
    else:
        if not args.manifest or args.gpu_evidence:
            parser.error("a manifest is required for execution; --gpu-evidence is attachment-only")
        manifest = json.loads(args.manifest.read_text())
        validate_manifest(manifest)
        # Exclusive creation protects previous lab artifacts, even if commands fail.
        args.output_dir.mkdir(parents=True, exist_ok=False)
        report = benchmark(manifest, args.cwd.resolve())
    (args.output_dir / "results.json").write_text(json.dumps(report, indent=2) + "\n")
    (args.output_dir / "report.md").write_text(render_markdown(report))
    print(args.output_dir / "report.md")
    return 1 if report.get("environment_collection_cancelled") or any(c["execution"]["status"] != "completed" or c["validation_status"] not in ("completed", "untested") for c in report["cases"]) else 0


if __name__ == "__main__":
    raise SystemExit(main())

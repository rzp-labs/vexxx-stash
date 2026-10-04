import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
import unittest
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))
import benchmark


class BenchmarkTests(unittest.TestCase):
    def assert_process_terminated(self, pid, timeout=.5):
        # Signals and init's orphan reaper are asynchronous. A missing stat file
        # after a successful PID probe requires another probe, not an assertion
        # on the now-vanished proc entry or permission to accept a live child.
        deadline = time.monotonic() + timeout
        while True:
            try:
                os.kill(pid, 0)
            except ProcessLookupError:
                return
            if sys.platform.startswith("linux"):
                try:
                    raw = Path(f"/proc/{pid}/stat").read_text()
                except FileNotFoundError:
                    pass
                else:
                    if raw.rsplit(") ", 1)[1].split()[0] == "Z":
                        return
            if time.monotonic() >= deadline:
                self.fail(f"process {pid} is still live after cleanup")
            time.sleep(.01)

    def command(self, code, timeout=2):
        return {"argv": [sys.executable, "-c", code], "timeout_seconds": timeout}

    def diagnostic_command(self, actual, selected=None, reason="", validation="passed"):
        record = {"workload": "marker", "diagnostics": [{"Selected": selected or actual, "Actual": actual, "Stage": "", "Reason": reason}], "output_validation": {"status": validation}}
        return self.command(f"print({json.dumps(record)!r})")

    def manifest(self, fixture, **case_values):
        case = {"id": "cpu", "selected_backend": "cpu", "actual_backend": "cpu",
                **self.command("print('fixture')"), **case_values}
        return {"schema_version": 1, "candidate_revision": "1" * 40,
                "environment": "local-cpu", "fixtures": [{"path": str(fixture), "source_kind": "synthetic"}],
                "cases": [case]}

    def test_child_success_and_resource_measurement(self):
        result = benchmark.run_command(self.command("sum(i*i for i in range(100000)); print('done')"), Path.cwd())
        self.assertEqual(result["status"], "completed")
        self.assertEqual(result["stdout"].strip(), "done")
        self.assertGreater(result["cpu_seconds"], 0)
        self.assertGreater(result["peak_rss_bytes"], 0)
        self.assertNotIn("HOME", result["environment"])

    def test_nonzero_and_launch_failure(self):
        result = benchmark.run_command(self.command("import sys; sys.stderr.write('no codec'); sys.exit(7)"), Path.cwd())
        self.assertEqual((result["status"], result["exit_code"]), ("failed", 7))
        self.assertEqual(result["stderr"], "no codec")
        result = benchmark.run_command({"argv": ["/definitely/missing/media-tool"]}, Path.cwd())
        self.assertEqual(result["status"], "launch_failed")

    def test_cancel_or_failure_during_child_setup_reaps_process(self):
        original_popen = subprocess.Popen
        original_selector = benchmark.selectors.DefaultSelector
        for interruption in ("sigterm", "sigint", "launch_sigterm", "launch_sigint", "setup_error"):
            with self.subTest(interruption=interruption):
                children = []

                def record_child(*args, **kwargs):
                    child = original_popen(*args, **kwargs)
                    children.append(child)
                    if interruption.startswith("launch_"):
                        os.kill(os.getpid(), signal.SIGTERM if interruption.endswith("sigterm") else signal.SIGINT)
                    return child

                def interrupt_setup():
                    if interruption == "setup_error":
                        raise OSError("selector setup failed")
                    os.kill(os.getpid(), signal.SIGTERM if interruption == "sigterm" else signal.SIGINT)
                    return original_selector()

                try:
                    with mock.patch.object(benchmark.subprocess, "Popen", side_effect=record_child), \
                         mock.patch.object(benchmark.selectors, "DefaultSelector", side_effect=interrupt_setup):
                        if interruption == "setup_error":
                            with self.assertRaisesRegex(OSError, "selector setup failed"):
                                benchmark.run_command(self.command("import time; time.sleep(30)"), Path.cwd())
                        else:
                            result = benchmark.run_command(self.command("import time; time.sleep(30)"), Path.cwd())
                            self.assertEqual(result["status"], "cancelled")
                    self.assertEqual(len(children), 1)
                    self.assertIsNotNone(children[0].returncode)
                    with self.assertRaises(ChildProcessError):
                        os.waitpid(children[0].pid, os.WNOHANG)
                    self.assertTrue(children[0].stdout.closed and children[0].stderr.closed)
                finally:
                    # Keep even a failing regression bounded and leave no child.
                    for child in children:
                        if child.returncode is None:
                            benchmark.kill_group(child.pid, signal.SIGKILL)
                            child.wait(timeout=3)
                        child.stdout.close()
                        child.stderr.close()

    def test_timeout_and_process_group_cleanup(self):
        with tempfile.TemporaryDirectory() as directory:
            pidfile = Path(directory) / "pid"
            # Descendant shares process group and holds stdout open after leader exits.
            code = ("import subprocess,sys,time; "
                    "p=subprocess.Popen([sys.executable,'-c','import time; time.sleep(30)']); "
                    f"open({str(pidfile)!r},'w').write(str(p.pid)); time.sleep(30)")
            start = time.monotonic()
            result = benchmark.run_command(self.command(code, .2), Path.cwd())
            self.assertEqual(result["status"], "timed_out")
            self.assertLess(time.monotonic() - start, 4)
            pid = int(pidfile.read_text())
            self.assert_process_terminated(pid)

    def test_cleanup_assertion_rechecks_pid_when_proc_entry_disappears(self):
        with mock.patch.object(sys, "platform", "linux"), \
             mock.patch.object(os, "kill", side_effect=[None, ProcessLookupError]) as probe, \
             mock.patch.object(Path, "read_text", side_effect=FileNotFoundError):
            self.assert_process_terminated(12345)
        self.assertEqual(probe.call_count, 2)

    def test_cleanup_assertion_accepts_terminated_linux_zombie(self):
        with mock.patch.object(sys, "platform", "linux"), \
             mock.patch.object(os, "kill", return_value=None), \
             mock.patch.object(Path, "read_text", return_value="12345 (worker with ) spaces) Z 1 12345"):
            self.assert_process_terminated(12345, timeout=0)

    def test_cleanup_assertion_rejects_live_process(self):
        with self.assertRaisesRegex(AssertionError, "still live"):
            self.assert_process_terminated(os.getpid(), timeout=0)
        # Missing procfs evidence alone cannot excuse a PID that stays live.
        with mock.patch.object(sys, "platform", "linux"), \
             mock.patch.object(os, "kill", return_value=None), \
             mock.patch.object(Path, "read_text", side_effect=FileNotFoundError), \
             self.assertRaisesRegex(AssertionError, "still live"):
            self.assert_process_terminated(12345, timeout=0)

    def test_output_is_bounded(self):
        result = benchmark.run_command(self.command("import sys; sys.stdout.write('x'*1000000); sys.stdout.flush()"), Path.cwd())
        self.assertEqual(result["status"], "output_limit")
        self.assertEqual(len(result["stdout"]), benchmark.OUTPUT_LIMIT)

    def test_hard_limits_and_private_environment_rejected(self):
        for spec in ({"argv": ["true"], "timeout_seconds": 56},
                     {"argv": ["true"], "timeout_seconds": float("nan")},
                     {"argv": ["true"], "env": {"SECRET": "must not be recorded"}},
                     {"argv": "echo unsafe"}):
            with self.assertRaises(ValueError):
                benchmark.command_spec(spec)

    def test_fallback_never_claims_gpu_and_validation_failure_recorded(self):
        with tempfile.TemporaryDirectory() as directory:
            fixture = Path(directory) / "synthetic.txt"
            fixture.write_text("generated test fixture")
            manifest = self.manifest(fixture, **self.diagnostic_command("software", "qsv", "permission denied"), selected_backend="qsv", actual_backend="cpu",
                                     fallback_reason="permission denied", gpu_evidence={"path": str(fixture), "confirmed_activity": True},
                                     validation=self.command("raise SystemExit(2)"))
            report = benchmark.benchmark(manifest, Path.cwd())
            self.assertEqual(report["cases"][0]["gpu_evidence_status"], "cpu_fallback")
            self.assertEqual(report["cases"][0]["validation_status"], "failed")
            self.assertEqual(report["responsiveness"]["status"], "untested")
            self.assertIn("B580 budgets: unagreed", benchmark.render_markdown(report))
            json.dumps(report, allow_nan=False)

    def test_driver_and_external_validation_preserve_failures_and_unknowns(self):
        with tempfile.TemporaryDirectory() as directory:
            fixture = Path(directory) / "synthetic.txt"
            fixture.write_text("synthetic fixture")
            for driver in ("failed", "error", "untested", "passed"):
                for exit_code in (0, 1):
                    with self.subTest(driver=driver, validator_exit=exit_code):
                        manifest = self.manifest(fixture, **self.diagnostic_command("software", validation=driver),
                                                 validation=self.command(f"raise SystemExit({exit_code})"))
                        case = benchmark.benchmark(manifest, Path.cwd())["cases"][0]
                        expected = "failed" if exit_code or driver in ("failed", "error") else ("untested" if driver == "untested" else "completed")
                        self.assertEqual(case["validation_status"], expected)
                        self.assertEqual(case["driver_output_validation"]["status"], driver)
                        self.assertEqual(case["validation"]["status"], "failed" if exit_code else "completed")

    def test_cli_failed_driver_check_cannot_be_overridden_by_validator(self):
        with tempfile.TemporaryDirectory() as directory:
            fixture = Path(directory) / "synthetic.txt"
            fixture.write_text("synthetic fixture")
            manifest = Path(directory) / "manifest.json"
            manifest.write_text(json.dumps(self.manifest(fixture, **self.diagnostic_command("software", validation="failed"),
                                                        validation=self.command("raise SystemExit(0)"))))
            output = Path(directory) / "results"
            result = benchmark.run_command({"argv": [sys.executable, str(Path(benchmark.__file__).resolve()), str(manifest), "--output-dir", str(output)]}, Path.cwd())
            self.assertEqual(result["exit_code"], 1)
            case = json.loads((output / "results.json").read_text())["cases"][0]
            self.assertEqual(case["validation_status"], "failed")
            self.assertEqual(case["validation"]["status"], "completed")
            self.assertEqual(case["driver_output_validation"]["status"], "failed")

    def test_gpu_requires_runtime_backend_new_samples_and_alignment(self):
        with tempfile.TemporaryDirectory() as directory:
            fixture = Path(directory) / "synthetic.txt"
            fixture.write_text("fixture")
            samples = Path(directory) / "gpu.json"
            samples.write_text('{"engines":{"Video/0":{"busy":25}}}\n{"engines":{"Video/0":{"busy":0}}}')
            manifest = self.manifest(fixture, selected_backend="qsv", actual_backend="qsv",
                                     gpu_evidence={"path": str(samples), "confirmed_activity": True})
            # Manifest assertions and an old positive sample never establish actual GPU work.
            report = benchmark.benchmark(manifest, Path.cwd())
            self.assertEqual(report["cases"][0]["gpu_evidence_status"], "unverified")
            self.assertEqual(report["cases"][0]["actual_backend"], "unknown")
            manifest["cases"][0].update(self.diagnostic_command("qsv"))
            report = benchmark.benchmark(manifest, Path.cwd())
            case = report["cases"][0]
            self.assertEqual(case["actual_backend"], "qsv")
            self.assertEqual(case["validation_status"], "completed")
            self.assertEqual(case["gpu_evidence_status"], "unverified")
            execution = case["execution"]
            mapping = {"cpu": {"path": str(samples), "case_id": "cpu", "candidate_revision": "1" * 40,
                       "command_started_unix": execution["started_unix"],
                       "monitor_start_unix": execution["started_unix"] - .1,
                       "monitor_end_unix": execution["ended_unix"] + .1}}
            enriched = benchmark.attach_gpu_evidence(report, mapping)
            self.assertEqual(enriched["cases"][0]["gpu_evidence_status"], "observed_video_activity")
            self.assertEqual(enriched["cases"][0]["gpu_evidence"]["sample_count"], 2)
            mapping["cpu"]["command_started_unix"] -= 10
            enriched = benchmark.attach_gpu_evidence(report, mapping)
            self.assertEqual(enriched["cases"][0]["gpu_evidence_status"], "unverified")
            samples.write_text('[{"engines":{"Render/0":{"busy":90},"Video/0":{"busy":0}}}]')
            self.assertFalse(benchmark.gpu_activity(samples)["video_activity_observed"])

    def test_mixed_queue_backend_is_per_job_and_fallbacks_remain_visible(self):
        with tempfile.TemporaryDirectory() as directory:
            fixture = Path(directory) / "fixture"
            fixture.write_text("synthetic")
            samples = Path(directory) / "gpu.json"
            samples.write_text('{"engines":{"Video/0":{"busy":25}}}')
            cpu = {"Selected": "software", "Actual": "software", "Stage": "", "Reason": "canonical CPU hash"}
            gpu = {"Selected": "qsv", "Actual": "qsv", "Stage": "", "Reason": ""}
            fallback = {"Selected": "qsv", "Actual": "software", "Stage": "decode", "Reason": "unsupported source"}
            record = {"workload": "mixed", "actual_backend": "per_job",
                      "output_validation": {"status": "passed", "jobs": [
                          {"name": "marker", "status": "passed", "selected_backend": "qsv", "actual_backend": "qsv", "diagnostics": [gpu]},
                          {"name": "sprites", "status": "passed", "selected_backend": "qsv", "actual_backend": "software", "diagnostics": [fallback]},
                          {"name": "phash", "status": "passed", "selected_backend": "software", "actual_backend": "software", "diagnostics": [cpu]}]}}
            for last in (cpu, gpu):
                record["diagnostics"] = [gpu, fallback, last]
                manifest = self.manifest(fixture, selected_backend="qsv", **self.command(f"print({json.dumps(record)!r})"))
                report = benchmark.benchmark(manifest, Path.cwd())
                case = report["cases"][0]
                self.assertEqual(case["actual_backend"], "per_job")
                self.assertEqual([j["actual_backend"] for j in case["per_job_backend_evidence"]], ["qsv", "cpu", "cpu"])
                self.assertEqual(case["per_job_fallbacks"][0]["fallback_reason"], "unsupported source")
                execution = case["execution"]
                mapping = {"cpu": {"path": str(samples), "case_id": "cpu", "candidate_revision": "1"*40,
                           "command_started_unix": execution["started_unix"],
                           "monitor_start_unix": execution["started_unix"] - .1,
                           "monitor_end_unix": execution["ended_unix"] + .1}}
                enriched = benchmark.attach_gpu_evidence(report, mapping)
                self.assertEqual(enriched["cases"][0]["gpu_evidence_status"], "unverified")
                self.assertTrue(enriched["cases"][0]["gpu_evidence"]["video_activity_observed"])
            self.assertEqual(benchmark.runtime_backend({"diagnostics": [gpu, {"Actual": "per_job"}, cpu]}), ("per_job", None))

    def test_gpu_partial_terminal_record_keeps_only_complete_prefix(self):
        with tempfile.TemporaryDirectory() as directory:
            samples = Path(directory) / "gpu.json"
            complete = '{"engines":{"Video/0":{"busy":12.5}}}'
            partial = '{"engines":{"Video/0":{"busy":99'
            for content in (complete + "\n" + partial, "[\n" + complete + ",\n" + partial):
                samples.write_text(content)
                result = benchmark.gpu_activity(samples)
                self.assertEqual(result["sample_count"], 1)
                self.assertEqual(result["peak_video_busy_percent"], 12.5)
                self.assertEqual(result["parse_status"], "complete_prefix_with_unparsed_tail")
                self.assertEqual(result["truncated_tail_bytes"], len(partial.encode()))
                self.assertTrue(result["parse_error"])
            samples.write_text("[" + complete)
            result = benchmark.gpu_activity(samples)
            self.assertEqual(result["sample_count"], 1)
            self.assertEqual(result["truncated_tail_bytes"], 0)
            self.assertIn("closing bracket", result["parse_error"])
            samples.write_text("[" + complete + "]")
            result = benchmark.gpu_activity(samples)
            self.assertEqual(result["parse_status"], "complete")
            self.assertIsNone(result["parse_error"])

    def test_gpu_malformed_array_delimiters_stop_at_valid_prefix(self):
        with tempfile.TemporaryDirectory() as directory:
            samples = Path(directory) / "gpu.json"
            first = '{"engines":{"Video/0":{"busy":12.5}}}'
            second = '{"engines":{"Video/0":{"busy":99}}}'
            for content in ("[" + first + ",," + second + "]",
                            "[" + first + ",]", "[" + first + second + "]"):
                samples.write_text(content)
                result = benchmark.gpu_activity(samples)
                self.assertEqual(result["sample_count"], 1)
                self.assertEqual(result["peak_video_busy_percent"], 12.5)
                self.assertEqual(result["parse_status"], "complete_prefix_with_unparsed_tail")
                self.assertGreater(result["truncated_tail_bytes"], 0)
                self.assertTrue(result["parse_error"])

    def test_gpu_malformed_without_complete_sample_fails(self):
        with tempfile.TemporaryDirectory() as directory:
            samples = Path(directory) / "gpu.json"
            for content in ("{not-json}", '{"engines":{"Video/0":{"busy":99', "[]", ""):
                samples.write_text(content)
                with self.assertRaises(ValueError):
                    benchmark.gpu_activity(samples)

    def test_runtime_validation_failure(self):
        with tempfile.TemporaryDirectory() as directory:
            fixture = Path(directory) / "synthetic.txt"
            fixture.write_text("fixture")
            report = benchmark.benchmark(self.manifest(fixture, **self.diagnostic_command("software", validation="failed")), Path.cwd())
            self.assertEqual(report["cases"][0]["validation_status"], "failed")
            self.assertEqual(report["cases"][0]["actual_backend"], "cpu")

    def test_sigterm_ignoring_child_is_killed_within_grace(self):
        start = time.monotonic()
        result = benchmark.run_command(self.command("import signal,time; signal.signal(signal.SIGTERM,signal.SIG_IGN); time.sleep(30)", .15), Path.cwd())
        self.assertEqual(result["status"], "timed_out")
        self.assertTrue(result["cleanup_sigkill_sent"])
        self.assertLess(time.monotonic() - start, 3.7)

    def test_external_sigterm_cancels_and_reaps_child(self):
        with tempfile.TemporaryDirectory() as directory:
            pidfile = Path(directory) / "pid"
            child_code = f"import os,time; open({str(pidfile)!r},'w').write(str(os.getpid())); time.sleep(30)"
            runner_code = (f"import sys; sys.path.insert(0,{str(Path(benchmark.__file__).resolve().parent)!r}); "
                           "import benchmark,pathlib,json; "
                           f"print(json.dumps(benchmark.run_command({self.command(child_code)!r},pathlib.Path.cwd())))")
            outer = subprocess.Popen([sys.executable, "-c", runner_code], stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            deadline = time.monotonic() + 3
            try:
                while not pidfile.exists() and time.monotonic() < deadline:
                    time.sleep(.01)
                self.assertTrue(pidfile.exists())
                pid = int(pidfile.read_text())
                outer.terminate()
                stdout, stderr = outer.communicate(timeout=2)
                self.assertEqual(outer.returncode, 0, stderr)
                self.assertEqual(json.loads(stdout)["status"], "cancelled")
                with self.assertRaises(ProcessLookupError):
                    os.kill(pid, 0)
            finally:
                if outer.poll() is None:
                    outer.kill()
                    outer.communicate()

    def test_environment_commands_record_versions_and_failures_without_skipping_jobs(self):
        with tempfile.TemporaryDirectory() as directory:
            fixture = Path(directory) / "fixture"
            fixture.write_text("synthetic")
            manifest = self.manifest(fixture)
            manifest["environment_commands"] = [
                {"name": "python-version", "argv": [sys.executable, "--version"]},
                {"name": "missing-version", "argv": ["/missing/version-tool"]}]
            report = benchmark.benchmark(manifest, Path.cwd())
            metadata = report["environment_commands"]
            self.assertEqual(metadata[0]["execution"]["status"], "completed")
            self.assertEqual(metadata[0]["execution"]["timeout_seconds"], 3)
            self.assertIn("Python", metadata[0]["execution"]["stdout"])
            self.assertEqual(metadata[1]["execution"]["status"], "launch_failed")
            self.assertEqual(report["cases"][0]["execution"]["status"], "completed")
            self.assertIn("python-version", benchmark.render_markdown(report))
            for invalid in ([{"name": "too-long", "argv": ["true"], "timeout_seconds": 3.1}],
                            [{"name": "duplicate", "argv": ["true"]}] * 2,
                            [{"name": str(i), "argv": ["true"]} for i in range(9)]):
                manifest["environment_commands"] = invalid
                with self.assertRaises(ValueError):
                    benchmark.validate_manifest(manifest)

    def test_duplicate_cases_and_nonsynthetic_sources_rejected(self):
        manifest = self.manifest(Path("fixture"))
        manifest["cases"].append(dict(manifest["cases"][0]))
        with self.assertRaises(ValueError):
            benchmark.validate_manifest(manifest)
        manifest["cases"].pop()
        manifest["fixtures"][0]["source_kind"] = "private-media"
        with self.assertRaises(ValueError):
            benchmark.validate_manifest(manifest)

    def test_cli_preserves_existing_output(self):
        with tempfile.TemporaryDirectory() as directory:
            fixture = Path(directory) / "synthetic.txt"
            fixture.write_text("synthetic")
            manifest = Path(directory) / "manifest.json"
            manifest.write_text(json.dumps(self.manifest(fixture)))
            output = Path(directory) / "report"
            result = benchmark.run_command({"argv": [sys.executable, str(Path(benchmark.__file__).resolve()), str(manifest), "--output-dir", str(output)]}, Path.cwd())
            self.assertEqual(result["status"], "completed")
            original = (output / "results.json").read_bytes()
            result = benchmark.run_command({"argv": [sys.executable, str(Path(benchmark.__file__).resolve()), str(manifest), "--output-dir", str(output)]}, Path.cwd())
            self.assertEqual(result["status"], "failed")
            self.assertEqual((output / "results.json").read_bytes(), original)


if __name__ == "__main__":
    unittest.main()

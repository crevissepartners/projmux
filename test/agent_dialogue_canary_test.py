"""No provider traffic: exercise the live canary's public-stream sanitizer."""
import concurrent.futures
import json
import os
import selectors
import shlex
import shutil
import sys
import threading
import pathlib
import subprocess
import tempfile
import unittest


class DialogueCollectorTest(unittest.TestCase):
    def setUp(self):
        source = (pathlib.Path(__file__).resolve().parents[1] /
                  "scripts/agent-dialogue-live-canary.sh").read_text()
        code = source.split("print(r'''#!/usr/bin/env python3\n", 1)[1].split("''')\nPY", 1)[0]
        self.root = tempfile.TemporaryDirectory(prefix="pmx-dialogue-collector-")
        self.addCleanup(self.root.cleanup)
        root = pathlib.Path(self.root.name)
        (root / "bin").mkdir()
        (root / "home/.claude").mkdir(parents=True)
        self.collector = root / "bin/collector"
        self.collector.write_text(code)
        commands = [
            "projmux internal agent-hook ingest claude-hook --pane=${PMX_INTERNAL_ACTIVATION_PANE_UID:-} >/dev/null 2>&1 || true # projmux-managed:claude-hook:v1",
            "exec projmux internal claude-endpoint-register >/dev/null 2>&1 # projmux-managed:claude-hook:v1",
        ]
        (root / "home/.claude/settings.json").write_text(json.dumps({"hooks": {
            "SessionStart": [{"hooks": [{"type": "command", "command": c} for c in commands]}]}}))
        self.rows = []
        for index, command in enumerate(commands):
            base = dict(type="system", hook_id=f"hook-{index}", hook_name=command,
                        hook_event="SessionStart", session_id="owned-session", uuid=f"event-{index}")
            self.rows.extend([dict(base, subtype="hook_started"), dict(base, subtype="hook_response",
                              output="", stdout="", stderr="", exit_code=0, outcome="success")])
        self.rows.append(dict(type="system", subtype="init", session_id="owned-session"))

    def collect(self, rows):
        return subprocess.run(["python3", str(self.collector)],
                              input="".join(json.dumps(row) + "\n" for row in rows),
                              text=True, capture_output=True, timeout=5, check=False)

    def test_tool_usage_and_nested_secret_shapes_fail_before_disk(self):
        assistant = dict(type="assistant", uuid="owned-assistant", session_id="owned-session",
                         message=dict(role="assistant", content=[dict(type="text", text="READY.")]))
        result = dict(type="result", subtype="success", is_error=False, session_id="owned-session", result="READY.")
        for bad_usage in ({"server_tool_use": {"web_fetch_requests": 1, "web_search_requests": 0}},
                          {"iterations": [{}]}, {"CLAUDE_CODE_MESSAGING_TOKEN": "DO_NOT_RETAIN"},
                          {"input_tokens": "DO_NOT_RETAIN"}):
            for event in (dict(result, usage=bad_usage), dict(assistant, message=dict(assistant["message"], usage=bad_usage))):
                output = self.collect(self.rows + [event])
                self.assertNotEqual(output.returncode, 0)
                self.assertNotIn("DO_NOT_RETAIN", output.stdout + output.stderr)
        self.assertNotEqual(self.collect(self.rows + [dict(result, structured_output={"token": "DO_NOT_RETAIN"})]).returncode, 0)
        good = dict(input_tokens=1, output_tokens=1, iterations=[], server_tool_use=dict(web_fetch_requests=0, web_search_requests=0))
        output = self.collect(self.rows + [dict(result, usage=good)])
        self.assertEqual(output.returncode, 0, output.stderr)
        self.assertEqual(json.loads(output.stdout.splitlines()[-1])["usage"], {})

    def test_usage_provider_strings_follow_public_schema_and_are_discarded(self):
        row = dict(inputTokens=1, outputTokens=1, webSearchRequests=0, costUSD=0,
                   canonicalModel="fixture model[context]", provider="DO_NOT_RETAIN", costBasis="list")
        result = dict(type="result", subtype="success", is_error=False, session_id="owned-session",
                      result="READY.", modelUsage={"fixture-model[context]": row},
                      usage=dict(inference_geo="", service_tier="fixture tier", speed="DO_NOT_RETAIN"))
        output = self.collect(self.rows + [result])
        self.assertEqual(output.returncode, 0, output.stderr)
        clean = json.loads(output.stdout.splitlines()[-1])
        self.assertEqual(clean["modelUsage"], {})
        self.assertEqual(clean["usage"], {})
        self.assertNotIn("DO_NOT_RETAIN", output.stdout + output.stderr)
        for change in ({"provider": []}, {"webSearchRequests": 1}, {"unknown": 0}, {"canonicalModel": "x" * 4097}):
            invalid = dict(result, modelUsage={"fixture-model[context]": dict(row, **change)})
            self.assertNotEqual(self.collect(self.rows + [invalid]).returncode, 0)

    def test_success_result_and_rate_metadata_are_validated_then_minimized(self):
        rate = dict(type="rate_limit_event", uuid="owned-rate", session_id="owned-session", rate_limit_info=dict(
            isUsingOverage=False, overageResetsAt=1, overageStatus="rejected", rateLimitType="five_hour", resetsAt=2,
            status="allowed", unifiedWindows={"five_hour": dict(resetsAt=2, utilization=0.1), "seven_day": dict(resetsAt=3, utilization=0.2)}))
        result = dict(type="result", subtype="success", is_error=False, session_id="owned-session", result="READY.",
                      permission_denials=[], terminal_reason="completed", stop_reason="end_turn", api_error_status=None,
                      queued_turn_count=0, duration_ms=1, first_content_frame_ms=1, fast_mode_state="off",
                      fast_mode_disabled_reason="sdk_opt_in_required")
        output = self.collect(self.rows + [rate, result])
        self.assertEqual(output.returncode, 0, output.stderr)
        clean = list(map(json.loads, output.stdout.splitlines()))
        self.assertEqual(clean[-2], dict(type="rate_limit_event", uuid="owned-rate", session_id="owned-session", metadataValidated=True))
        self.assertTrue(clean[-1]["metadataValidated"])
        self.assertNotIn("terminal_reason", clean[-1])
        for change in ({"is_error": True}, {"api_error_status": 403}, {"queued_turn_count": 1},
                       {"permission_denials": [{}]}, {"subagent_stats": {"spawned": 1}}, {"terminal_reason": "api_error"}):
            self.assertNotEqual(self.collect(self.rows + [dict(result, **change)]).returncode, 0)
        rate["rate_limit_info"]["unifiedWindows"]["five_hour"]["utilization"] = "invalid"
        self.assertNotEqual(self.collect(self.rows + [rate]).returncode, 0)

    def test_closed_hook_and_refusal_diagnostics_do_not_admit_events(self):
        events = [dict(self.rows[0], hook_event=kind) for kind in ("UserPromptSubmit", "Stop")]
        events += [dict(type="system", subtype=kind, content="DO_NOT_RETAIN")
                   for kind in ("model_refusal_fallback", "model_refusal_no_fallback")]
        for event in events:
            output = self.collect(self.rows + [event])
            self.assertNotEqual(output.returncode, 0)
            self.assertNotIn("DO_NOT_RETAIN", output.stdout + output.stderr)
            diagnostic = json.loads(output.stderr.split(": ", 1)[1])
            if "hook_event" in event:
                self.assertEqual(diagnostic["hookEventMatch"], event["hook_event"])
            else:
                self.assertEqual(diagnostic["subtype"], event["subtype"])

    def test_paired_owned_startup_strips_empty_output(self):
        result = self.collect(self.rows)
        self.assertEqual(result.returncode, 0, result.stderr)
        for event in map(json.loads, result.stdout.splitlines()):
            self.assertFalse({"output", "stdout", "stderr"} & set(event))

    def test_foreign_or_incomplete_startup_fails_without_retaining_output(self):
        for mutation in ("alias", "output", "session", "pending", "plugin", "unknown", "setup"):
            with self.subTest(mutation=mutation):
                rows = [dict(row) for row in self.rows]
                if mutation == "alias":
                    rows[0]["hook_name"] = "unobserved-name"
                elif mutation == "output":
                    rows[1]["output"] = "DO_NOT_RETAIN"
                elif mutation == "session":
                    rows[1]["session_id"] = "foreign"
                elif mutation == "pending":
                    rows.pop(1)
                elif mutation == "plugin":
                    rows[0]["subtype"] = "plugin_install"
                elif mutation == "unknown":
                    rows[0]["unobserved-field"] = "DO_NOT_RETAIN"
                elif mutation == "setup":
                    rows[0]["hook_event"] = "Setup"
                result = self.collect(rows)
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn("DO_NOT_RETAIN", result.stdout + result.stderr)
                self.assertNotIn("unobserved-field", result.stderr)

    def test_public_thinking_content_and_signature_are_removed_before_output(self):
        assistant = dict(type="assistant", uuid="owned-assistant", session_id="owned-session",
                         parent_tool_use_id=None, message=dict(role="assistant", context_management=None,
                         diagnostics=None, stop_details=None, content=[dict(type="thinking",
                         thinking="PRIVATE_THOUGHT", signature="PRIVATE_SIGNATURE"), dict(type="text", text="fixture reply")]))
        result = self.collect(self.rows + [assistant])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn("PRIVATE_THOUGHT", result.stdout + result.stderr)
        self.assertNotIn("PRIVATE_SIGNATURE", result.stdout + result.stderr)
        content = json.loads(result.stdout.splitlines()[-1])["message"]["content"]
        self.assertEqual(content, [dict(type="thinking", contentOmitted=True), dict(type="text", text="fixture reply")])
        assistant["message"]["diagnostics"] = {}
        self.assertNotEqual(self.collect(self.rows + [assistant]).returncode, 0)
        assistant["message"]["diagnostics"] = None
        assistant["message"]["content"][0]["type"] = "tool_use"
        self.assertNotEqual(self.collect(self.rows + [assistant]).returncode, 0)

    def test_assistant_wrapper_metadata_does_not_bypass_content_gate(self):
        assistant = dict(type="assistant", uuid="owned-assistant", session_id="owned-session",
                         parent_tool_use_id=None, request_id="req_fixture", timestamp="2026-09-06T00:00:00Z",
                         message=dict(role="assistant", content=[dict(type="text", text="fixture reply")]))
        self.assertEqual(self.collect(self.rows + [assistant]).returncode, 0)
        assistant["message"]["unobserved_field"] = "DO_NOT_RETAIN"
        result = self.collect(self.rows + [assistant])
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("unobserved_field", result.stderr)
        self.assertNotIn("DO_NOT_RETAIN", result.stdout + result.stderr)

    def test_numeric_progress_is_same_session_post_init_metadata_only(self):
        progress = dict(type="system", subtype="thinking_tokens", estimated_tokens=12,
                        estimated_tokens_delta=2, uuid="owned-progress", session_id="owned-session")
        self.assertEqual(self.collect(self.rows + [progress]).returncode, 0)
        for change in ({"session_id": "foreign"}, {"estimated_tokens": True},
                       {"estimated_tokens_delta": "DO_NOT_RETAIN"}, {"text": "DO_NOT_RETAIN"}):
            result = self.collect(self.rows + [dict(progress, **change)])
            self.assertNotEqual(result.returncode, 0)
            self.assertNotIn("DO_NOT_RETAIN", result.stdout + result.stderr)
        self.assertNotEqual(self.collect([progress] + self.rows).returncode, 0)

    def test_observed_init_metadata_has_no_permission_authority(self):
        rows = [dict(row) for row in self.rows]
        rows[-1].update(capabilities=["interrupt_receipt_v1"],
                        fast_mode_disabled_reason="sdk_opt_in_required",
                        analytics_disabled=True, product_feedback_disabled=False)
        self.assertEqual(self.collect(rows).returncode, 0)
        for key, invalid in (("capabilities", ["unknown value with spaces"]),
                             ("fast_mode_disabled_reason", "unobserved-reason"),
                             ("analytics_disabled", 1), ("product_feedback_disabled", "false")):
            with self.subTest(key=key):
                changed = [dict(row) for row in rows]
                changed[-1][key] = invalid
                self.assertNotEqual(self.collect(changed).returncode, 0)

    def test_observed_startup_display_alias_keeps_two_distinct_hook_pairs(self):
        rows = [dict(row) for row in self.rows]
        for row in rows[:-1]:
            row["hook_name"] = "SessionStart:startup"
        result = self.collect(rows)
        self.assertEqual(result.returncode, 0, result.stderr)
        rows[2]["hook_id"] = rows[0]["hook_id"]
        self.assertNotEqual(self.collect(rows).returncode, 0)


@unittest.skipUnless(hasattr(os, "pidfd_open"), "live canary cleanup requires Linux pidfd")
class DialogueCleanupTest(unittest.TestCase):
    def setUp(self):
        self.source = (pathlib.Path(__file__).resolve().parents[1] /
                       "scripts/agent-dialogue-live-canary.sh").read_text()
        code = self.source.split("<<'WRITERS_PY' || return 1\n", 1)[1].split("\nWRITERS_PY\n", 1)[0]
        self.code = {"__name__": "canary_cleanup_test"}
        exec(compile(code, "canary-cleanup", "exec"), self.code)
        self.temp = tempfile.TemporaryDirectory(prefix="pmx-writer-cleanup-")
        self.addCleanup(self.temp.cleanup)
        self.root = pathlib.Path(self.temp.name, "owned")
        self.root.mkdir()

    def writer(self, root_argument=True):
        program = ("import pathlib,sys; print('ready',flush=True); sys.stdin.readline(); "
                   "p=pathlib.Path(sys.argv[1]); p.mkdir(parents=True,exist_ok=True); "
                   "(p/'termination-receipts.jsonl').write_text('late owned receipt\\n')")
        target = self.root / "xdg-state/projmux" if root_argument else pathlib.Path(self.temp.name, "foreign")
        child = subprocess.Popen([sys.executable, "-c", program, str(target)],
                                 stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        self.assertEqual(child.stdout.readline().strip(), "ready")
        self.addCleanup(self.release, child)
        return child

    @staticmethod
    def release(child):
        if child.poll() is None:
            child.communicate("release\n", timeout=5)
        else:
            child.communicate(timeout=5)

    def test_failure_cleanup_waits_for_delayed_writer_before_removing_root(self):
        child = self.writer()
        (self.root / "cleanup-plan.json").write_text(json.dumps({"version": 3, "ownedRoot": str(self.root)}))
        (self.root / ".projmux-dialogue-canary-owned").write_text("projmux-dialogue-canary-owned-v3\n")
        identity = self.root.stat()
        finish = "finish_cleanup() {" + self.source.split("finish_cleanup() {", 1)[1].split("\ntrap finish_cleanup EXIT", 1)[0]
        library = pathlib.Path(self.temp.name, "cleanup.py")
        library.write_text(self.source.split("<<'WRITERS_PY' || return 1\n", 1)[1].split("\nWRITERS_PY\n", 1)[0])
        client = pathlib.Path(self.temp.name, "finish.py")
        client.write_text("import os,pathlib,runpy,sys; n=runpy.run_path(sys.argv[1]); "
                          "n['close_owned_writers'](pathlib.Path(sys.argv[2]),lambda _:None, "
                          "on_wait=lambda:os.write(int(sys.argv[3]),b'w'))")
        read_fd, write_fd = os.pipe()
        command = " ".join(map(shlex.quote, [sys.executable, str(client), str(library), str(self.root), str(write_fd)]))
        shell = ("set -euo pipefail\nroot=" + shlex.quote(str(self.root)) +
                 f"\nroot_identity={identity.st_dev}:{identity.st_ino}\n" +
                 "cleanup_owned() { " + command + "; }\n" + finish + "\ntrap finish_cleanup EXIT\nfalse\n")
        cleanup = subprocess.Popen(["bash", "-c", shell], pass_fds=(write_fd,),
                                   stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        os.close(write_fd)
        try:
            with selectors.DefaultSelector() as poller:
                poller.register(read_fd, selectors.EVENT_READ)
                self.assertTrue(poller.select(5), "cleanup did not reach the process-exit barrier")
            self.assertEqual(os.read(read_fd, 1), b"w")
            self.assertTrue(self.root.exists())
            self.assertIsNone(cleanup.poll())
            self.assertIsNone(child.poll())
        finally:
            os.close(read_fd)
            self.release(child)
        stdout, stderr = cleanup.communicate(timeout=5)
        self.assertEqual(cleanup.returncode, 1, (stdout, stderr))  # Preserve the original canary failure.
        self.assertFalse(self.root.exists())
        self.assertEqual(child.returncode, 0)

    def test_captured_writer_stays_owned_after_parent_exit(self):
        read_fd, write_fd = os.pipe()
        program = ("import os,pathlib,sys; pid=os.fork(); "
                   "p=pathlib.Path(sys.argv[1]); "
                   "print('parent' if pid else 'child',flush=True); "
                   "sys.stdin.readline() if pid else os.read(int(sys.argv[2]),1); "
                   "sys.exit(0) if pid else None; "
                   "p.mkdir(parents=True,exist_ok=True); (p/'receipt').write_text('late')")
        parent = subprocess.Popen([sys.executable, "-c", program, str(self.root / "state"), str(read_fd)],
                                  pass_fds=(read_fd,), stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        os.close(read_fd)
        self.assertEqual({parent.stdout.readline().strip(), parent.stdout.readline().strip()}, {"parent", "child"})
        waiting = threading.Event()
        def teardown(_):
            parent.stdin.write("exit\n"); parent.stdin.flush()
        with concurrent.futures.ThreadPoolExecutor(max_workers=1) as executor:
            future = executor.submit(self.code["close_owned_writers"], self.root, teardown, (), 5, waiting.set)
            try:
                self.assertTrue(waiting.wait(5))
                parent.wait(timeout=5)
                self.assertFalse(future.done())
                self.assertTrue(self.root.exists())
            finally:
                os.write(write_fd, b"x"); os.close(write_fd)
            proof = future.result(timeout=5)
        parent.communicate(timeout=5)
        self.assertGreaterEqual(len(proof["writers"]), 2)
        self.assertTrue((self.root / "state/receipt").exists())

    def test_stubborn_writer_times_out_without_removing_root_or_signalling_it(self):
        child = self.writer()
        with self.assertRaisesRegex(RuntimeError, "root retained"):
            self.code["close_owned_writers"](self.root, lambda _: None, timeout=0.05)
        self.assertTrue(self.root.exists())
        self.assertIsNone(child.poll())

    def test_foreign_or_replaced_birth_is_neither_waited_on_nor_signalled(self):
        child = self.writer(root_argument=False)
        observed = self.code["OwnedWriterBarrier"].observe(child.pid)[0]
        for replacement in (dict(observed, start=observed["start"] + "-replaced"),
                            dict(observed, ownerUID=observed["ownerUID"] + 1)):
            proof = self.code["close_owned_writers"](self.root, lambda _: None, (replacement,), timeout=0.05)
            self.assertEqual(proof["writers"], [])
            self.assertIsNone(child.poll())

    def test_real_exit_trap_retains_root_when_writer_proof_fails(self):
        root = self.root
        credentials = root / "home/.claude/.credentials.json"
        credentials.parent.mkdir(parents=True)
        credentials.write_text("fixture only")
        finish = "finish_cleanup() {" + self.source.split("finish_cleanup() {", 1)[1].split("\ntrap finish_cleanup EXIT", 1)[0]
        shell = "set -euo pipefail\nroot=" + shlex.quote(str(root)) + "\ncleanup_owned() { return 1; }\n" + finish + "\ntrap finish_cleanup EXIT\nfalse\n"
        result = subprocess.run(["bash", "-c", shell], text=True, capture_output=True, timeout=5)
        self.assertNotEqual(result.returncode, 0)
        self.assertTrue(root.exists())
        self.assertFalse(credentials.exists())
        self.assertIn("root retained", result.stderr)

if __name__ == "__main__":
    unittest.main()

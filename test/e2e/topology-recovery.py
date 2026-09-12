#!/usr/bin/env python3
"""Check the last committed recovery invocation from the public diagnostics CLI."""

import json
import os
import pathlib
import sys
import tempfile

log, stderr, resumed, skipped, expected_reasons = sys.argv[1:]
events = [json.loads(line) for line in pathlib.Path(log).read_text().splitlines()]
outcomes = [event for event in events if event["event"] == "topology.outcome"]
assert outcomes, "missing topology outcome"
outcome = outcomes[-1]
assert outcome["result"] == "success", outcome
assert outcome["resumed_count"] == int(resumed), outcome
assert outcome["skipped_count"] == int(skipped), outcome
same_run = [event for event in events if event["run_id"] == outcome["run_id"]]
assert sum(event["event"] == "topology.outcome" for event in same_run) == 1, same_run
reasons = [event for event in same_run if event["event"] == "topology.agent.skipped"]
assert len({event["code"] for event in reasons}) == len(reasons), reasons
assert sum(event["item_count"] for event in reasons) == int(skipped), reasons
assert {event["code"]: event["item_count"] for event in reasons} == json.loads(expected_reasons), reasons
allowed = {"at", "level", "component", "event", "result", "duration_ms", "run_id", "version", "mux_backend", "resumed_count", "skipped_count", "item_count", "code"}
assert all(set(event) <= allowed for event in same_run), same_run
summary = pathlib.Path(stderr).read_text().splitlines()[-1]
assert len(summary.encode("utf-8")) <= 220, summary
assert f"resumed {resumed}, skipped {skipped};" in summary or f"복귀 {resumed}, 건너뜀 {skipped};" in summary, summary
assert "projmux diagnostics log --component topology" in summary, summary
# Retain only verified closed events and the exact content-free summary. Source
# stderr contains private per-Agent details and must never be copied here.
assert summary in (
    f"Continue: resumed {resumed}, skipped {skipped}; projmux diagnostics log --component topology",
    f"Continue: 복귀 {resumed}, 건너뜀 {skipped}; projmux diagnostics log --component topology",
), summary
artifact_root = pathlib.Path(os.environ["PROJMUX_E2E_ARTIFACTS"])
artifact_root.mkdir(parents=True, exist_ok=True)
with tempfile.NamedTemporaryFile(mode="w", encoding="utf-8", prefix="topology-recovery-", suffix=".json", dir=artifact_root, delete=False) as artifact:
    json.dump({"events": same_run, "summary": summary}, artifact, ensure_ascii=False, indent=2)
    artifact.write("\n")
print(f"recovery run={outcome['run_id']} resumed={resumed} skipped={skipped} reasons={len(reasons)} summary_bytes={len(summary.encode('utf-8'))}")

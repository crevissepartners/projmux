#!/usr/bin/env python3
"""Validate the checked-in E2E AGS-OEDR and lower-layer evidence graph."""

from __future__ import annotations

import argparse
import hashlib
import json
import pathlib
import re
import sys
from typing import Any


EXPECTED_IDS = [*[f"L{index:02d}" for index in range(1, 20)], "C01", "N01"]
SOURCE_BY_SUITE = {
    "linux": "test/e2e/linux-smoke.sh",
    "codex": "test/e2e/codex-lifecycle.sh",
    "npm": "test/e2e/npm-staging-path.sh",
}
REQUIRED_AGS_OEDR = ("A", "G", "S", "O", "E", "D", "R")
EXPECTED_MERGED_EVIDENCE = {
    "L17.merged-766": {
        "scenario_id": "L17",
        "source_commit": "fc04577e7b5908ecb3d73f3694d32d174c519ac3",
        "guarantees": {
            "simultaneous_or_coalesced_exit",
            "exact_generation_cleanup",
            "offline_resumable_refs",
            "sibling_preservation",
            "fixed_point",
        },
    },
    "L18.merged-767": {
        "scenario_id": "L18",
        "source_commit": "217bdc32f51f36914625df92bfb17de7abe1e03d",
        "guarantees": {
            "foreground_producer_matrix",
            "exact_client_delivery",
            "success_silent_no_overlay",
            "origin_identity_preservation",
            "bounded_refusal_failure",
        },
    },
}
BEGIN_RE = re.compile(
    r"^smoke_contract_begin\s+(?P<id>[LCN]\d{2})\s+(?P<scenario>[a-z0-9-]+)\s+(?P<owner>[a-z0-9-]+)\s*$",
    re.MULTILINE,
)


def fail(message: str) -> None:
    raise ValueError(message)


def closed_relative_path(root: pathlib.Path, raw: Any, label: str) -> pathlib.Path:
    if not isinstance(raw, str) or not raw or pathlib.PurePosixPath(raw).is_absolute():
        fail(f"{label} must be a non-empty repository-relative path")
    path = pathlib.PurePosixPath(raw)
    if ".." in path.parts or path.as_posix() != raw:
        fail(f"{label} is not canonical: {raw}")
    resolved = root / raw
    if not resolved.is_file() or resolved.is_symlink():
        fail(f"{label} does not name a checked-in regular file: {raw}")
    return resolved


def nonempty_string(value: Any, label: str) -> str:
    if not isinstance(value, str) or not value.strip():
        fail(f"{label} must be a non-empty string")
    return value


def load_manifest(path: pathlib.Path) -> dict[str, Any]:
    document = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(document, dict):
        fail("manifest root must be an object")
    if document.get("schema") != "projmux.e2e.ags-oedr.v1":
        fail("manifest schema is not projmux.e2e.ags-oedr.v1")
    return document


def validate_go_test_reference(
    root: pathlib.Path,
    item: Any,
    label: str,
) -> tuple[str, str]:
    if not isinstance(item, dict) or set(item) != {"path", "symbol", "selector"}:
        fail(f"{label} must contain exact path/symbol/selector fields")
    relative = nonempty_string(item.get("path"), f"{label}.path")
    lower_path = closed_relative_path(root, relative, f"{label}.path")
    if lower_path.suffix != ".go" or not lower_path.name.endswith("_test.go"):
        fail(f"{label}.path must name a Go test source")
    symbol = nonempty_string(item.get("symbol"), f"{label}.symbol")
    lower_text = lower_path.read_text(encoding="utf-8")
    if not re.search(rf"^func\s+{re.escape(symbol)}\s*\(", lower_text, re.MULTILINE):
        fail(f"{label} referenced test symbol disappeared: {symbol}")
    selector = nonempty_string(item.get("selector"), f"{label}.selector")
    package = pathlib.PurePosixPath(relative).parent.as_posix()
    expected_selector = f"go test ./{package} -run ^{symbol}$ -count=1"
    if selector != expected_selector:
        fail(f"{label} exact lower-layer selector differs: {selector}")
    return relative, symbol


def source_inventory(root: pathlib.Path) -> dict[str, dict[str, str]]:
    inventory: dict[str, dict[str, str]] = {}
    for suite, relative in SOURCE_BY_SUITE.items():
        text = (root / relative).read_text(encoding="utf-8")
        for match in BEGIN_RE.finditer(text):
            scenario_id = match.group("id")
            if scenario_id in inventory:
                fail(f"duplicate stable scenario marker: {scenario_id}")
            inventory[scenario_id] = {
                "suite": suite,
                "source": relative,
                "scenario": match.group("scenario"),
                "owner": match.group("owner"),
            }
    return inventory


def shard_inventory(root: pathlib.Path) -> dict[str, str]:
    result: dict[str, str] = {}
    for line in (root / "test/e2e/linux-shards.tsv").read_text(encoding="utf-8").splitlines():
        if not line.strip():
            continue
        shard, raw_ids = line.split("\t")
        for scenario_id in raw_ids.split():
            if scenario_id in result:
                fail(f"duplicate shard assignment: {scenario_id}")
            result[scenario_id] = shard
    expected = EXPECTED_IDS[:19]
    if sorted(result) != sorted(expected):
        fail(f"Linux shard inventory differs: actual={sorted(result)} expected={expected}")
    if len(set(result.values())) != 4:
        fail("Linux inventory must use exactly four shards")
    return result


def validate(root: pathlib.Path, manifest: dict[str, Any]) -> dict[str, Any]:
    stable = manifest.get("stable_inventory")
    if stable != EXPECTED_IDS:
        fail("stable_inventory must be exact ordered L01-L19,C01,N01")
    scenarios = manifest.get("scenarios")
    if not isinstance(scenarios, list):
        fail("scenarios must be an array")
    scenario_ids = [item.get("id") if isinstance(item, dict) else None for item in scenarios]
    if len(scenario_ids) != len(set(scenario_ids)):
        fail("manifest contains duplicate scenario ids")

    sources = source_inventory(root)
    source_ids = set(sources)
    manifest_ids = set(scenario_ids)
    expected_ids = set(EXPECTED_IDS)
    orphan_source = sorted(source_ids - manifest_ids)
    orphan_manifest = sorted(manifest_ids - source_ids)
    if source_ids != expected_ids or manifest_ids != expected_ids:
        fail(
            f"scenario orphan audit failed: source_only={orphan_source} "
            f"manifest_only={orphan_manifest} source_inventory={sorted(source_ids)}"
        )

    shards = shard_inventory(root)
    for item in scenarios:
        if not isinstance(item, dict):
            fail("every scenario must be an object")
        scenario_id = item["id"]
        observed = sources[scenario_id]
        suite = nonempty_string(item.get("suite"), f"{scenario_id}.suite")
        source = nonempty_string(item.get("source"), f"{scenario_id}.source")
        if suite not in SOURCE_BY_SUITE or source != SOURCE_BY_SUITE[suite]:
            fail(f"{scenario_id} suite/source is not canonical")
        closed_relative_path(root, source, f"{scenario_id}.source")
        if observed["suite"] != suite or observed["source"] != source:
            fail(f"{scenario_id} source marker maps to a different suite/source")
        if item.get("O") != observed["owner"]:
            fail(f"{scenario_id} owner differs from executable begin marker")
        for key in ("axis", "layer", "e2e_boundary", *REQUIRED_AGS_OEDR):
            nonempty_string(item.get(key), f"{scenario_id}.{key}")
        if scenario_id.startswith("L"):
            if item.get("shard") != shards[scenario_id]:
                fail(f"{scenario_id} shard differs from linux-shards.tsv")
        elif item.get("shard") not in ("codex-lifecycle", "npm-staging"):
            fail(f"{scenario_id} has invalid suite shard")

    moved = manifest.get("moved_matrices")
    if not isinstance(moved, list) or not moved:
        fail("moved_matrices must contain at least one evidence-backed move")
    moved_ids: set[str] = set()
    moved_markers: set[str] = set()
    for index, item in enumerate(moved):
        if not isinstance(item, dict):
            fail(f"moved_matrices[{index}] must be an object")
        moved_id = nonempty_string(item.get("id"), f"moved_matrices[{index}].id")
        if moved_id in moved_ids:
            fail(f"duplicate moved matrix id: {moved_id}")
        moved_ids.add(moved_id)
        scenario_id = nonempty_string(item.get("scenario_id"), f"{moved_id}.scenario_id")
        if scenario_id not in manifest_ids:
            fail(f"{moved_id} references unknown scenario {scenario_id}")
        former = item.get("former_e2e_cells")
        sentinels = item.get("e2e_sentinel_cells")
        if not isinstance(former, int) or not isinstance(sentinels, int) or former <= sentinels or sentinels < 1:
            fail(f"{moved_id} has invalid former/sentinel cell counts")
        marker = nonempty_string(item.get("source_marker"), f"{moved_id}.source_marker")
        moved_markers.add(marker)
        scenario_source = root / SOURCE_BY_SUITE[sources[scenario_id]["suite"]]
        if scenario_source.read_text(encoding="utf-8").count(marker) != 1:
            fail(f"{moved_id} source marker is missing or duplicated")
        lower = item.get("lower_layer")
        if not isinstance(lower, dict) or lower.get("layer") not in ("unit", "integration"):
            fail(f"{moved_id} lower_layer is not executable unit/integration evidence")
        lower_path = closed_relative_path(root, lower.get("path"), f"{moved_id}.lower_layer.path")
        symbol = nonempty_string(lower.get("symbol"), f"{moved_id}.lower_layer.symbol")
        cell_count_symbol = nonempty_string(
            lower.get("cell_count_symbol"), f"{moved_id}.lower_layer.cell_count_symbol"
        )
        selector = nonempty_string(lower.get("selector"), f"{moved_id}.lower_layer.selector")
        lower_text = lower_path.read_text(encoding="utf-8")
        if not re.search(rf"^func\s+{re.escape(symbol)}\s*\(", lower_text, re.MULTILINE):
            fail(f"{moved_id} referenced test symbol disappeared: {symbol}")
        if not re.search(
            rf"^const\s+{re.escape(cell_count_symbol)}\s*=\s*{former}\s*$",
            lower_text,
            re.MULTILINE,
        ):
            fail(
                f"{moved_id} lower-layer cell count differs: "
                f"{cell_count_symbol} must equal {former}"
            )
        if not re.search(
            rf"cells\s*!=\s*{re.escape(cell_count_symbol)}",
            lower_text,
        ):
            fail(f"{moved_id} lower-layer test does not assert its executed cell count")
        expected_selector = f"go test ./internal/app -run ^{symbol}$ -count=1"
        if selector != expected_selector:
            fail(f"{moved_id} exact lower-layer selector differs: {selector}")
        for evidence_kind in ("positive", "negative", "fixed_point"):
            nonempty_string(lower.get(evidence_kind), f"{moved_id}.lower_layer.{evidence_kind}")
        nonempty_string(item.get("e2e_sentinel"), f"{moved_id}.e2e_sentinel")

    merged = manifest.get("merged_evidence")
    if not isinstance(merged, list):
        fail("merged_evidence must be an array")
    merged_ids = [item.get("id") if isinstance(item, dict) else None for item in merged]
    if len(merged_ids) != len(set(merged_ids)):
        fail("merged_evidence contains duplicate ids")
    if set(merged_ids) != set(EXPECTED_MERGED_EVIDENCE):
        fail(
            "merged_evidence inventory differs: "
            f"actual={sorted(str(item) for item in merged_ids)} "
            f"expected={sorted(EXPECTED_MERGED_EVIDENCE)}"
        )
    merged_lower_refs: set[tuple[str, str]] = set()
    merged_lower_count = 0
    for item in merged:
        if not isinstance(item, dict) or set(item) != {
            "id", "scenario_id", "source_commit", "guarantees", "lower_layer", "integration", "e2e"
        }:
            fail("merged_evidence row has an open or incomplete field set")
        evidence_id = str(item["id"])
        expected_merged = EXPECTED_MERGED_EVIDENCE[evidence_id]
        scenario_id = nonempty_string(item.get("scenario_id"), f"{evidence_id}.scenario_id")
        if scenario_id != expected_merged["scenario_id"] or scenario_id not in manifest_ids:
            fail(f"{evidence_id} scenario_id differs from its closed merged source")
        if item.get("source_commit") != expected_merged["source_commit"]:
            fail(f"{evidence_id} source_commit differs from the merged main evidence")
        guarantees = item.get("guarantees")
        if not isinstance(guarantees, dict) or set(guarantees) != expected_merged["guarantees"]:
            fail(f"{evidence_id} guarantee inventory differs")
        for guarantee, description in guarantees.items():
            nonempty_string(description, f"{evidence_id}.guarantees.{guarantee}")

        lower_layer = item.get("lower_layer")
        if not isinstance(lower_layer, list) or not lower_layer:
            fail(f"{evidence_id}.lower_layer must contain executable tests")
        local_refs: set[tuple[str, str]] = set()
        for lower_index, lower in enumerate(lower_layer):
            ref = validate_go_test_reference(root, lower, f"{evidence_id}.lower_layer[{lower_index}]")
            if ref in local_refs or ref in merged_lower_refs:
                fail(f"duplicate merged lower-layer test reference: {ref}")
            local_refs.add(ref)
            merged_lower_refs.add(ref)
            merged_lower_count += 1

        for layer in ("integration", "e2e"):
            boundary = item.get(layer)
            if not isinstance(boundary, dict) or set(boundary) != {"path", "marker"}:
                fail(f"{evidence_id}.{layer} must contain exact path/marker fields")
            expected_path = "test/integration/linux-smoke.sh" if layer == "integration" else sources[scenario_id]["source"]
            if boundary.get("path") != expected_path:
                fail(f"{evidence_id}.{layer}.path differs: {boundary.get('path')}")
            boundary_path = closed_relative_path(root, expected_path, f"{evidence_id}.{layer}.path")
            marker = nonempty_string(boundary.get("marker"), f"{evidence_id}.{layer}.marker")
            boundary_text = boundary_path.read_text(encoding="utf-8")
            if boundary_text.count(marker) != 1:
                fail(f"{evidence_id}.{layer} marker is missing or duplicated")
            if layer == "e2e":
                begin = re.search(rf"^smoke_contract_begin\s+{scenario_id}\s+", boundary_text, re.MULTILINE)
                if begin is None:
                    fail(f"{evidence_id} has no executable E2E begin marker")
                next_begin = re.search(r"^smoke_contract_begin\s+", boundary_text[begin.end():], re.MULTILINE)
                end = begin.end() + next_begin.start() if next_begin else len(boundary_text)
                scenario_body = boundary_text[begin.start():end]
                marker_offset = scenario_body.find(marker)
                pass_offset = scenario_body.find("smoke_contract_pass", marker_offset + len(marker))
                if marker_offset < 0 or pass_offset < 0:
                    fail(f"{evidence_id} marker is not enforced before its E2E pass")

    manifest_bytes = json.dumps(manifest, ensure_ascii=False, separators=(",", ":"), sort_keys=True).encode()
    return {
        "schema": "projmux.e2e.coverage-audit.v1",
        "scenario_count": len(scenarios),
        "linux_shard_count": len(set(shards.values())),
        "moved_matrix_count": len(moved),
        "moved_former_cells": sum(item["former_e2e_cells"] for item in moved),
        "moved_e2e_sentinel_cells": sum(item["e2e_sentinel_cells"] for item in moved),
        "merged_evidence_count": len(merged),
        "merged_lower_test_count": merged_lower_count,
        "orphan_count": 0,
        "manifest_sha256": hashlib.sha256(manifest_bytes).hexdigest(),
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--manifest", default="test/e2e/ags-oedr-manifest.json")
    parser.add_argument("--output")
    args = parser.parse_args()
    root = pathlib.Path.cwd().resolve()
    manifest_path = closed_relative_path(root, args.manifest, "manifest")
    summary = validate(root, load_manifest(manifest_path))
    rendered = json.dumps(summary, separators=(",", ":"), sort_keys=True) + "\n"
    if args.output:
        output = pathlib.Path(args.output)
        output.parent.mkdir(parents=True, exist_ok=True)
        output.write_text(rendered, encoding="utf-8")
    sys.stdout.write(rendered)
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (KeyError, OSError, TypeError, ValueError, json.JSONDecodeError) as error:
        print(f"e2e coverage: {error}", file=sys.stderr)
        raise SystemExit(1)

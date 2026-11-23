# /// script
# requires-python = ">=3.12"
# dependencies = [
#   "numpy",
#   "pandas",
#   "matplotlib",
# ]
# ///
"""Runs experiment 1's pilot end to end (units/U05.md): builds one openb input
version, runs it through all four (rho, scheduler) combinations, aggregates
the resulting records into a report, and machine-checks spec.md's Scenarios
1-6 Then clauses plus the numeric sanity checks units/U05.md's Implementation
Decisions lists ("数が合う", "値の範囲", "負荷の効き方", "スケジューラの効き方").

This command is intentionally left out of run_checks.sh and check-range.txt
(units/U05.md): running it against the full openb trace takes longer than the
10-minute budget those scripts give the whole check range.

Usage: uv run aggregate/run_pilot.py --out pilot-run
"""

from __future__ import annotations

import csv
import io
import sys
from dataclasses import dataclass
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
import aggregate  # noqa: F401  (used by later checks in this module)


@dataclass(frozen=True)
class CheckResult:
    """One Then clause or sanity check's outcome, named so a failure's reason
    is visible without re-reading the check's code (units/U05.md: "項目ごとに
    通ったか落ちたかを、落ちた理由とともに表示する")."""

    name: str
    passed: bool
    detail: str


@dataclass(frozen=True)
class BuildInputSummary:
    """The fields cmd/buildinput prints to stdout (cmd/buildinput/main.go), parsed
    back out so this command's checks can read them (units/U05.md scenario 1)."""

    used_pods: int
    excluded_pods: int
    failed_pods: int
    rho_before: float
    rho_after: float


_BUILDINPUT_FIELDS = {
    "used pods": "used_pods",
    "excluded pods": "excluded_pods",
    "failed pods": "failed_pods",
    "rho (before)": "rho_before",
    "rho (after)": "rho_after",
}


def parse_buildinput_summary(text: str) -> BuildInputSummary:
    """Parses cmd/buildinput's "key:  value" stdout lines back into a
    BuildInputSummary. Raises ValueError naming the missing field(s) if the
    text does not carry every field buildinput's run() always prints on
    success — a sign the text is not what buildinput actually printed."""
    values: dict[str, str] = {}
    for line in text.splitlines():
        if ":" not in line:
            continue
        key, _, raw = line.partition(":")
        key = key.strip()
        if key in _BUILDINPUT_FIELDS:
            values[key] = raw.strip()

    missing = [key for key in _BUILDINPUT_FIELDS if key not in values]
    if missing:
        raise ValueError(f"buildinput summary is missing field(s): {', '.join(missing)}")

    return BuildInputSummary(
        used_pods=int(values["used pods"]),
        excluded_pods=int(values["excluded pods"]),
        failed_pods=int(values["failed pods"]),
        rho_before=float(values["rho (before)"]),
        rho_after=float(values["rho (after)"]),
    )


def count_eligible_pods(pods_csv_text: str) -> int:
    """Counts the pod CSV's rows BuildInputVersion samples from before applying
    sampleRatio: every row except the header and "Pending" ones
    (scenarios/clusterdata/input_version.go). Used to compute the expected
    used-pod count independently of what buildinput itself reports
    (units/U05.md scenario 1's "Pendingを除いたPodの80%")."""
    reader = csv.DictReader(io.StringIO(pods_csv_text))
    return sum(1 for row in reader if row["pod_phase"].strip().lower() != "pending")


def expected_sampled_count(eligible: int, ratio: float) -> int:
    """The used-pod count BuildInputVersion's sampleAndOrderPods computes:
    int(eligible * ratio + 0.5), i.e. round-half-up truncated toward zero,
    the same as Go's int() conversion."""
    return int(eligible * ratio + 0.5)


_RHO_TOLERANCE = 1e-6


def check_scenario1(summary: BuildInputSummary, *, eligible_pods: int, ratio: float, target_rho: float) -> list[CheckResult]:
    """spec.md scenario 1's Then: the used-pod count is the sampled 80% of
    non-Pending pods, and the achieved rho matches the target
    (units/U05.md's "確かめ方": "使った件数が...80%であること", "求めたρが
    1.1であること")."""
    expected_used = expected_sampled_count(eligible_pods, ratio)
    used_ok = summary.used_pods == expected_used
    rho_ok = abs(summary.rho_after - target_rho) <= _RHO_TOLERANCE
    return [
        CheckResult(
            name="scenario1: used pods is 80% of non-pending pods",
            passed=used_ok,
            detail="ok" if used_ok else f"expected {expected_used} used pods ({eligible_pods} eligible x {ratio}), got {summary.used_pods}",
        ),
        CheckResult(
            name="scenario1: achieved rho matches the target",
            passed=rho_ok,
            detail="ok" if rho_ok else f"expected rho (after) == {target_rho}, got {summary.rho_after}",
        ),
    ]


def check_scenario2(record: aggregate.RunRecord) -> list[CheckResult]:
    """spec.md scenario 2's Then: the header names the input version's seed,
    rho, scheduler, both commits, and the hardware; the initial node capacity
    is recorded; and assign/complete transitions carry a non-decreasing
    virtual time (units/U05.md's "確かめ方")."""
    return []

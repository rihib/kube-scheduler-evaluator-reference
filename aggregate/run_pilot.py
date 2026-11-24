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
import math
import re
import sys
from collections.abc import Sequence
from dataclasses import dataclass
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
import aggregate


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


def _parse_buildinput_summary(text: str) -> BuildInputSummary:
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


def _count_eligible_pods(pods_csv_text: str) -> int:
    """Counts the pod CSV's rows BuildInputVersion samples from before applying
    sampleRatio: every row except the header and "Pending" ones
    (scenarios/clusterdata/input_version.go). Used to compute the expected
    used-pod count independently of what buildinput itself reports
    (units/U05.md scenario 1's "Pendingを除いたPodの80%")."""
    reader = csv.DictReader(io.StringIO(pods_csv_text))
    return sum(1 for row in reader if row["pod_phase"].strip().lower() != "pending")


def _expected_sampled_count(eligible: int, ratio: float) -> int:
    """The used-pod count BuildInputVersion's sampleAndOrderPods computes:
    int(eligible * ratio + 0.5), i.e. round-half-up truncated toward zero,
    the same as Go's int() conversion."""
    return int(eligible * ratio + 0.5)


_RHO_TOLERANCE = 1e-6


def _check_scenario1(summary: BuildInputSummary, *, eligible_pods: int, ratio: float, target_rho: float) -> list[CheckResult]:
    """spec.md scenario 1's Then: the used-pod count is the sampled 80% of
    non-Pending pods, and the achieved rho matches the target
    (units/U05.md's "確かめ方": "使った件数が...80%であること", "求めたρが
    1.1であること")."""
    expected_used = _expected_sampled_count(eligible_pods, ratio)
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


def _check_scenario2(record: aggregate.RunRecord) -> list[CheckResult]:
    """spec.md scenario 2's Then: the header names the input version's seed,
    rho, scheduler, both commits, and the hardware; the initial node capacity
    is recorded; and assign/complete transitions carry a non-decreasing
    virtual time (units/U05.md's "確かめ方")."""
    h = record.header
    header_ok = bool(h.input_seed and h.scheduler_profile and h.evaluator_commit and h.reference_commit and h.hardware) and h.rho > 0
    has_capacity = any(t.operation == aggregate.OpRegisterNode and t.state == aggregate.StateResourceCapacity for t in record.transitions)
    times = [aggregate.parse_time(t.time) for t in record.transitions]
    monotonic = all(times[i] <= times[i + 1] for i in range(len(times) - 1))

    # The time-order check above cannot stand in for this one: a run where
    # nothing was ever placed has no assign/complete transition to order, so
    # it would satisfy "non-decreasing" vacuously while carrying none of the
    # placement history scenario 2 is about. parse_time raising would have
    # already stopped the checks, so a found transition's time is parseable.
    missing_ops = [
        op for op in (aggregate.OpAssignNode, aggregate.OpComplete) if not any(t.operation == op and t.time for t in record.transitions)
    ]
    placement_ok = not missing_ops
    return [
        CheckResult(
            name="scenario2: header names seed, rho, scheduler, commits, and hardware",
            passed=header_ok,
            detail="ok" if header_ok else f"header incomplete: {h!r}",
        ),
        CheckResult(
            name="scenario2: initial node capacity is recorded",
            passed=has_capacity,
            detail="ok" if has_capacity else "no register_node/resource_capacity transition found",
        ),
        CheckResult(
            name="scenario2: assign and complete transitions are recorded with a virtual time",
            passed=placement_ok,
            detail="ok" if placement_ok else f"no transition with a virtual time found for operation(s): {missing_ops}",
        ),
        CheckResult(
            name="scenario2: transitions carry non-decreasing virtual time",
            passed=monotonic,
            detail="ok" if monotonic else "transition times are not in non-decreasing order",
        ),
    ]


def _check_scenario3(records: Sequence[aggregate.RunRecord]) -> list[CheckResult]:
    """spec.md scenario 3's Then: exactly four records exist, one per distinct
    (rho, scheduler profile) combination (units/U05.md's "確かめ方")."""
    count_ok = len(records) == 4
    combos = [(r.header.rho, r.header.scheduler_profile) for r in records]
    distinct_ok = len(set(combos)) == len(combos)
    return [
        CheckResult(
            name="scenario3: four records exist",
            passed=count_ok,
            detail="ok" if count_ok else f"expected 4 records, got {len(records)}",
        ),
        CheckResult(
            name="scenario3: the four records' (rho, scheduler) combinations are all distinct",
            passed=distinct_ok,
            detail="ok" if distinct_ok else f"combinations are not all distinct: {combos}",
        ),
    ]


_FIGURE_REF_RE = re.compile(r"!\[[^\]]*\]\(([^)]+)\)")


def _check_scenario4(report_text: str, *, existing_figures: set[str]) -> list[CheckResult]:
    """spec.md scenario 4's Then: the report carries the per-run metrics table
    and a real scheduler diff for every rho section, and every figure the
    report links to actually exists (units/U05.md's "確かめ方")."""
    rho_sections = re.split(r"(?m)^## ρ = ", report_text)[1:]

    # The five metrics units/U05.md scenario 4 names, as the columns
    # aggregate.py's _render_per_run_table writes them: クラスごとの待機時間
    # is the nonzero_wait_fraction column (its per-class breakdown lives in
    # the distribution figure, checked separately below), 配置されずに残った
    # Podの数 is pending_count_at_end, and the remaining three are
    # cluster_std, allocation_rate:<resource>, and unplaceable_free:<resource>.
    metrics_ok = bool(rho_sections) and all(
        "### 実行ごとのメトリクス" in s
        and "pending_count_at_end" in s
        and "cluster_std" in s
        and "nonzero_wait_fraction" in s
        and "allocation_rate:" in s
        and "unplaceable_free:" in s
        for s in rho_sections
    )
    diff_ok = bool(rho_sections) and all("### スケジューラの差" in s and "| metric | diff |" in s for s in rho_sections)

    referenced_figures = set(_FIGURE_REF_RE.findall(report_text))
    missing_figures = referenced_figures - existing_figures
    figures_ok = not missing_figures

    # The per-class wait times units/U05.md scenario 4 asks for appear only in
    # the distribution figure, so a rho section that links to no figure has
    # lost them — and "every referenced figure exists" above would pass
    # vacuously for zero references.
    sections_without_figure = [s.splitlines()[0] for s in rho_sections if not _FIGURE_REF_RE.search(s)]
    section_figures_ok = bool(rho_sections) and not sections_without_figure

    return [
        CheckResult(
            name="scenario4: every rho section has the five-metric per-run table",
            passed=metrics_ok,
            detail="ok" if metrics_ok else "a rho section is missing the per-run metrics table or one of its columns",
        ),
        CheckResult(
            name="scenario4: every rho section links to a wait-time distribution figure",
            passed=section_figures_ok,
            detail="ok"
            if section_figures_ok
            else (
                f"rho section(s) with no figure reference: {sections_without_figure}"
                if rho_sections
                else "the report has no rho section at all"
            ),
        ),
        CheckResult(
            name="scenario4: every rho section has a real scheduler diff",
            passed=diff_ok,
            detail="ok" if diff_ok else "a rho section's scheduler diff is the fallback message, not an actual diff table",
        ),
        CheckResult(
            name="scenario4: every figure the report links to exists",
            passed=figures_ok,
            detail="ok" if figures_ok else f"missing figure file(s): {sorted(missing_figures)}",
        ),
    ]


def _rho_section(report_text: str, rho_label: str) -> str | None:
    """Returns the "## ρ = <rho_label>" section's own text (up to the next "##
    ρ = " heading or end of report), or None if that rho has no section."""
    match = re.search(rf"(?m)^## ρ = {re.escape(rho_label)}\n(.*?)(?=^## ρ = |\Z)", report_text, re.DOTALL)
    return match.group(1) if match else None


def _check_scenario5(report_text: str) -> list[CheckResult]:
    """spec.md scenario 5's Then: the ρ = 0.5 section shows the nonzero-wait
    fraction for (at least) two schedulers, and the wait-time diff between
    them (units/U05.md's "確かめ方")."""
    section = _rho_section(report_text, "0.5")
    has_section = section is not None and "### 陰性対照" in section
    fraction_block = ""
    if has_section and "待機時間が0でないPodの割合:" in section:
        fraction_block = section.split("待機時間が0でないPodの割合:", 1)[1].split("待機時間の差:")[0]
    # ".+" (not "\S+"): a real scheduler_profile is a long description with
    # spaces (schedulerprofile.Pilot's "NodeResourcesFit(scoringStrategy=
    # LeastAllocated, ...); NodeResourcesBalancedAllocation disabled"), not a
    # single bare token — found running against real openb data
    # (units/U05.md's "先に抜き出す割合を小さくした入力で1回通す" step).
    scheduler_count = len(re.findall(r"(?m)^- .+: ", fraction_block))
    has_two_schedulers = scheduler_count >= 2
    has_diff = bool(section and "待機時間の差" in section and "wait:" in section)
    return [
        CheckResult(
            name="scenario5: rho = 0.5 has a negative-control section",
            passed=has_section,
            detail="ok" if has_section else "no ### 陰性対照 section found under ## ρ = 0.5",
        ),
        CheckResult(
            name="scenario5: at least two schedulers' nonzero-wait fraction is shown",
            passed=has_two_schedulers,
            detail="ok" if has_two_schedulers else f"found {scheduler_count} scheduler line(s), expected at least 2",
        ),
        CheckResult(
            name="scenario5: the wait-time diff between schedulers is shown",
            passed=has_diff,
            detail="ok" if has_diff else "no 待機時間の差 with a wait: entry found",
        ),
    ]


def _check_scenario6(report_text: str) -> list[CheckResult]:
    """spec.md scenario 6's Then: the ρ = 1.1 section shows the prediction/result
    pairing for large-request pods' wait time and the unplaceable free amount
    (units/U05.md's "確かめ方")."""
    section = _rho_section(report_text, "1.1")
    has_section = section is not None and "### 予測と結果（高負荷）" in section
    has_wait_row = bool(section and re.search(r"\| wait:\S+ \|.*\| (当たった|当たらなかった) \|", section))
    has_unplaceable_row = bool(section and re.search(r"\| unplaceable_free:\S+ \|.*\| (当たった|当たらなかった) \|", section))
    return [
        CheckResult(
            name="scenario6: rho = 1.1 has a prediction-and-result section",
            passed=has_section,
            detail="ok" if has_section else "no ### 予測と結果（高負荷） section found under ## ρ = 1.1",
        ),
        CheckResult(
            name="scenario6: a large-request wait-time prediction/result row is shown",
            passed=has_wait_row,
            detail="ok" if has_wait_row else "no wait: row with a 当たった/当たらなかった verdict found",
        ),
        CheckResult(
            name="scenario6: an unplaceable-free prediction/result row is shown",
            passed=has_unplaceable_row,
            detail="ok" if has_unplaceable_row else "no unplaceable_free: row with a 当たった/当たらなかった verdict found",
        ),
    ]


def _check_sanity_counts(record: aggregate.RunRecord, *, expected_pod_count: int, label: str) -> list[CheckResult]:
    """units/U05.md's "数が合う": every used pod has a create_task transition,
    and at the end of observation every pod is either "completed" or
    "pending" (not stuck "running" or, since this pilot has no preemption,
    "deleted").

    label (e.g. "rho=1.1, LeastAllocated") disambiguates the result when the
    caller runs this once per (rho, scheduler) combination: without it, all
    four combinations' results shared the exact same two names, so a failure
    in one combination could not be told apart from the other three passing
    (rihib:interface-judge round 2's test rewrite found this: a test that
    broke only the ρ=1.1/LeastAllocated combination picked up the unrelated
    ρ=0.5/LeastAllocated combination's passing result instead, by name)."""
    created = {
        t.component_id for t in record.transitions if t.operation == aggregate.OpCreateTask and t.state == aggregate.StateInstanceStatus
    }
    count_ok = len(created) == expected_pod_count

    statuses = aggregate.final_instance_statuses(record.transitions)
    bad = {instance_id: status for instance_id, status in statuses.items() if status not in ("completed", "pending")}
    final_ok = not bad

    return [
        CheckResult(
            name=f"sanity: created pod count matches the expected used-pod count ({label})",
            passed=count_ok,
            detail="ok" if count_ok else f"expected {expected_pod_count} created pods, found {len(created)}",
        ),
        CheckResult(
            name=f"sanity: every pod's final status is completed or pending ({label})",
            passed=final_ok,
            detail="ok" if final_ok else f"pod(s) with an unexpected final status: {bad}",
        ),
    ]


def _check_sanity_ranges(metrics: aggregate.Metrics, *, label: str) -> list[CheckResult]:
    """units/U05.md's "値の範囲": wait times and unplaceable_free are >= 0,
    allocation_rate is within [0, 1], and no metric is NaN.

    label disambiguates the result across the four (rho, scheduler)
    combinations the caller runs this for, the same reason _check_sanity_counts
    takes one."""
    all_waits = [w for waits in metrics.pending_wait_by_class.values() for w in waits]
    waits_ok = all(w >= 0 for w in all_waits)
    allocation_ok = all(0.0 <= v <= 1.0 for v in metrics.allocation_rate.values())
    unplaceable_ok = all(v >= 0 for v in metrics.unplaceable_free.values())

    scalar_values = [
        metrics.cluster_std,
        metrics.nonzero_wait_fraction,
        *metrics.allocation_rate.values(),
        *metrics.unplaceable_free.values(),
        *all_waits,
    ]
    nan_values = [v for v in scalar_values if v is not None and math.isnan(v)]
    nan_ok = not nan_values

    return [
        CheckResult(
            name=f"sanity: pending wait times are >= 0 ({label})",
            passed=waits_ok,
            detail="ok" if waits_ok else f"negative wait time(s) found: {[w for w in all_waits if w < 0]}",
        ),
        CheckResult(
            name=f"sanity: allocation_rate is within [0, 1] ({label})",
            passed=allocation_ok,
            detail="ok" if allocation_ok else f"allocation_rate out of range: {metrics.allocation_rate}",
        ),
        CheckResult(
            name=f"sanity: unplaceable_free is >= 0 ({label})",
            passed=unplaceable_ok,
            detail="ok" if unplaceable_ok else f"negative unplaceable_free: {metrics.unplaceable_free}",
        ),
        CheckResult(
            name=f"sanity: no metric is NaN ({label})",
            passed=nan_ok,
            detail="ok" if nan_ok else f"found {len(nan_values)} NaN value(s)",
        ),
    ]


def _check_sanity_rho_effect(metrics_low_rho: aggregate.Metrics, metrics_high_rho: aggregate.Metrics, *, scheduler: str) -> list[CheckResult]:
    """units/U05.md's "負荷の効き方": for every resource both runs measured and
    at least one of them actually allocated against, the higher rho's
    allocation_rate is higher than the lower rho's (same scheduler profile,
    since the caller compares matching schedulers).

    A resource both runs measured as exactly 0.0 is excluded from that
    comparison: every node's capacity carries a "pods" entry
    (scenarios/clusterdata/gpu2023.go's buildNode sets corev1.ResourcePods),
    but nothing ever allocates against it (only cpu/memory/gpu get
    allocated_resource transitions), so its allocation_rate is always 0.0
    regardless of rho — found running against real openb data
    (units/U05.md's "先に抜き出す割合を小さくした入力で1回通す" step).

    scheduler names the result (e.g. "sanity: rho effect on allocation_rate
    (LeastAllocated)"), matching the caller's own material-missing fallback
    name: without it, LeastAllocated's and MostAllocated's results were
    otherwise indistinguishable in the printed output (both named "sanity:
    rho effect on allocation_rate", with no way to tell which scheduler a
    failure's detail was about)."""
    name = f"sanity: rho effect on allocation_rate ({scheduler})"
    common = sorted(set(metrics_low_rho.allocation_rate) & set(metrics_high_rho.allocation_rate))
    exercised = [r for r in common if metrics_low_rho.allocation_rate[r] > 0 or metrics_high_rho.allocation_rate[r] > 0]
    if not exercised:
        return [
            CheckResult(
                name=name,
                passed=False,
                detail="no resource both runs measured was ever actually allocated against",
            )
        ]
    not_increased = {
        r: (metrics_low_rho.allocation_rate[r], metrics_high_rho.allocation_rate[r])
        for r in exercised
        if not metrics_high_rho.allocation_rate[r] > metrics_low_rho.allocation_rate[r]
    }
    ok = not not_increased
    return [
        CheckResult(
            name=name,
            passed=ok,
            detail="ok" if ok else f"allocation_rate did not increase from rho=0.5 to rho=1.1 for: {not_increased}",
        )
    ]


def _check_sanity_rho_achieved(summary: BuildInputSummary, *, target_rho: float) -> CheckResult:
    """units/U05.md's "負荷の効き方": the achieved rho (buildinput's "rho
    (after)") matches the target rho that run was built for. Reusable for
    both rho=0.5 and rho=1.1 (scenario 1's own check only covers rho=1.1)."""
    ok = abs(summary.rho_after - target_rho) <= _RHO_TOLERANCE
    return CheckResult(
        name=f"sanity: rho={target_rho} achieved matches the target",
        passed=ok,
        detail="ok" if ok else f"expected rho (after) == {target_rho}, got {summary.rho_after}",
    )


def _check_sanity_scheduler_effect(
    metrics_least_allocated: aggregate.Metrics, metrics_most_allocated: aggregate.Metrics, *, rho: float
) -> list[CheckResult]:
    """units/U05.md's "スケジューラの効き方": at the same rho, LeastAllocated's
    Cluster_std (lower is more evenly spread) is smaller than MostAllocated's,
    standing in for "配置先が異なる" since node names are not compared
    (testing.md: kube-scheduler breaks ties at random).

    rho names the result (e.g. "sanity: scheduler effect on cluster_std
    (rho=0.5)"), matching the caller's own material-missing fallback name:
    without it, the ρ = 0.5 and ρ = 1.1 results were otherwise
    indistinguishable in the printed output."""
    name = f"sanity: scheduler effect on cluster_std (rho={rho})"
    least_std = metrics_least_allocated.cluster_std
    most_std = metrics_most_allocated.cluster_std
    if least_std is None or most_std is None:
        return [
            CheckResult(
                name=name,
                passed=False,
                detail=f"cluster_std not measured for one or both schedulers (LeastAllocated={least_std}, MostAllocated={most_std})",
            )
        ]
    ok = least_std < most_std
    return [
        CheckResult(
            name=name,
            passed=ok,
            detail="ok" if ok else f"expected cluster_std(LeastAllocated) < cluster_std(MostAllocated), got {least_std} >= {most_std}",
        )
    ]


def run_all_checks(
    *,
    buildinput_summary_low_rho: BuildInputSummary,
    buildinput_summary_high_rho: BuildInputSummary,
    eligible_pods: int,
    ratio: float,
    records: Sequence[aggregate.RunRecord],
    report_text: str,
    existing_figures: set[str],
) -> list[CheckResult]:
    """Runs every spec.md Scenario 1-6 Then check and every units/U05.md
    numeric sanity check against one pilot run's actual outputs. This is the
    "検査とThenを確かめる処理" units/U05.md's Implementation Decisions commits
    to keeping separate from the files themselves: every argument here is
    already-parsed content, never a path, so this function never touches
    disk and is fully exercised by small built, expected, and broken inputs.

    When a check's input record is missing (the earlier pipeline stage that
    should have produced it failed, or produced the wrong combination), that
    check is reported as a single failed CheckResult naming what is missing,
    instead of raising or being silently skipped (units/U05.md: "前の段が
    落ちて後の項目の材料ができないときは、後の項目を「材料が無い」として
    落とす")."""
    results: list[CheckResult] = []
    results += _check_scenario1(buildinput_summary_high_rho, eligible_pods=eligible_pods, ratio=ratio, target_rho=aggregate.RHO_HIGH_LOAD)
    results.append(_check_sanity_rho_achieved(buildinput_summary_low_rho, target_rho=aggregate.RHO_NEGATIVE_CONTROL))
    results.append(_check_sanity_rho_achieved(buildinput_summary_high_rho, target_rho=aggregate.RHO_HIGH_LOAD))

    def find(rho: float, scheduler: str) -> aggregate.RunRecord | None:
        # scenarios/schedulerprofile.Pilot's description is not the bare
        # "LeastAllocated"/"MostAllocated" (schedulerprofile.go: "NodeResourcesFit
        # (scoringStrategy=LeastAllocated, ...); NodeResourcesBalancedAllocation
        # disabled"), so this matches by containment, not equality.
        for r in records:
            if math.isclose(r.header.rho, rho, abs_tol=_RHO_TOLERANCE) and scheduler in r.header.scheduler_profile:
                return r
        return None

    combos = [
        (aggregate.RHO_NEGATIVE_CONTROL, "LeastAllocated"),
        (aggregate.RHO_NEGATIVE_CONTROL, "MostAllocated"),
        (aggregate.RHO_HIGH_LOAD, "LeastAllocated"),
        (aggregate.RHO_HIGH_LOAD, "MostAllocated"),
    ]
    expected_pod_count = _expected_sampled_count(eligible_pods, ratio)
    metrics_by_combo: dict[tuple[float, str], aggregate.Metrics] = {}
    for rho, scheduler in combos:
        record = find(rho, scheduler)
        if record is None:
            results.append(
                CheckResult(
                    name=f"sanity: record for rho={rho}, {scheduler} exists",
                    passed=False,
                    detail=f"material missing: no record found for rho={rho}, {scheduler}",
                )
            )
            continue
        label = f"rho={rho}, {scheduler}"
        results += _check_sanity_counts(record, expected_pod_count=expected_pod_count, label=label)
        metrics = aggregate.compute_metrics(record.transitions)
        metrics_by_combo[(rho, scheduler)] = metrics
        results += _check_sanity_ranges(metrics, label=label)

    rec_high_least = find(aggregate.RHO_HIGH_LOAD, "LeastAllocated")
    if rec_high_least is not None:
        results += _check_scenario2(rec_high_least)
    else:
        results.append(
            CheckResult(
                name="scenario2: record for rho=1.1, LeastAllocated exists",
                passed=False,
                detail="material missing: no record found for rho=1.1, LeastAllocated",
            )
        )

    results += _check_scenario3(list(records))
    results += _check_scenario4(report_text, existing_figures=existing_figures)
    results += _check_scenario5(report_text)
    results += _check_scenario6(report_text)

    for scheduler in ("LeastAllocated", "MostAllocated"):
        m_low = metrics_by_combo.get((aggregate.RHO_NEGATIVE_CONTROL, scheduler))
        m_high = metrics_by_combo.get((aggregate.RHO_HIGH_LOAD, scheduler))
        if m_low is not None and m_high is not None:
            results += _check_sanity_rho_effect(m_low, m_high, scheduler=scheduler)
        else:
            results.append(
                CheckResult(
                    name=f"sanity: rho effect on allocation_rate ({scheduler})",
                    passed=False,
                    detail=f"material missing: both rho=0.5 and rho=1.1 records for {scheduler} are needed",
                )
            )

    for rho in (aggregate.RHO_NEGATIVE_CONTROL, aggregate.RHO_HIGH_LOAD):
        m_least = metrics_by_combo.get((rho, "LeastAllocated"))
        m_most = metrics_by_combo.get((rho, "MostAllocated"))
        if m_least is not None and m_most is not None:
            results += _check_sanity_scheduler_effect(m_least, m_most, rho=rho)
        else:
            results.append(
                CheckResult(
                    name=f"sanity: scheduler effect on cluster_std (rho={rho})",
                    passed=False,
                    detail=f"material missing: both schedulers' records at rho={rho} are needed",
                )
            )

    return results


def _format_results(results: Sequence[CheckResult]) -> str:
    """Renders every check's outcome as one line, so a failure's reason is
    visible without re-reading this module (units/U05.md: "項目ごとに通ったか
    落ちたかを、落ちた理由とともに表示する")."""
    return "\n".join(f"[{'PASS' if r.passed else 'FAIL'}] {r.name}: {r.detail}" for r in results)


def _parse_args(argv: list[str]):
    import argparse

    parser = argparse.ArgumentParser(description="Run experiment 1's pilot end to end and machine-check its Scenarios (units/U05.md).")
    parser.add_argument("--nodes", default=".cache/clusterdata/openb_node_list_all_node.csv", help="openb node list CSV")
    parser.add_argument("--pods", default=".cache/clusterdata/openb_pod_list_default.csv", help="openb pod list CSV")
    parser.add_argument("--seed", type=int, default=1, help="seed for sampling and ordering pods (cmd/buildinput, cmd/runpilot)")
    parser.add_argument("--ratio", type=float, default=0.8, help="fraction of non-Pending pods to keep")
    parser.add_argument("--out", default="pilot-run", help="directory the records/ and report/ subdirectories are written under")
    return parser.parse_args(argv)


def _run_subprocess(args: list[str]):
    import subprocess

    return subprocess.run(args, capture_output=True, text=True, check=False)


_PILOT_RHOS = (aggregate.RHO_NEGATIVE_CONTROL, aggregate.RHO_HIGH_LOAD)
# The exact scheduler_profile header value cmd/runpilot's LeastAllocated runs
# carry (scenarios/schedulerprofile/schedulerprofile.go's description format).
_LEAST_ALLOCATED_DESCRIPTION = (
    "NodeResourcesFit(scoringStrategy=LeastAllocated, resources=cpu:1,memory:1,nvidia.com/gpu:1); NodeResourcesBalancedAllocation disabled"
)
_MISSING_BUILDINPUT_SUMMARY = BuildInputSummary(
    used_pods=-1, excluded_pods=-1, failed_pods=-1, rho_before=float("nan"), rho_after=float("nan")
)


def run(argv: list[str], *, run_subprocess=None) -> int:
    """The pilot command (units/U05.md): builds one openb input version per
    rho (cmd/buildinput, run once per rho so scenario 1's own numbers are
    for rho=1.1 while the sanity checks also see rho=0.5's), runs all four
    (rho, scheduler) combinations (cmd/runpilot), aggregates them into a
    report (aggregate.run, in-process since both are this same Python
    module), and machine-checks every Scenario 1-6 Then plus the numeric
    sanity checks (run_all_checks). Must be run from the repository root, so
    "go run ./cmd/..." resolves.

    run_subprocess is injected so a test can observe or fake the "go run"
    calls without actually invoking Go; the default runs them for real.
    """
    run_subprocess = run_subprocess or _run_subprocess
    args = _parse_args(argv)
    out_dir = Path(args.out)
    records_dir = out_dir / "records"
    report_dir = out_dir / "report"

    try:
        pods_text = Path(args.pods).read_text(encoding="utf-8")
    except OSError as e:
        print(f"run_pilot: failed to read {args.pods}: {e}", file=sys.stderr)
        return 1
    eligible_pods = _count_eligible_pods(pods_text)

    summaries: dict[float, BuildInputSummary] = {}
    for rho in _PILOT_RHOS:
        proc = run_subprocess(
            [
                "go",
                "run",
                "./cmd/buildinput",
                "--nodes",
                args.nodes,
                "--pods",
                args.pods,
                "--seed",
                str(args.seed),
                "--rho",
                str(rho),
                "--ratio",
                str(args.ratio),
            ]
        )
        if proc.returncode != 0:
            print(f"run_pilot: buildinput failed for rho={rho}:\n{proc.stderr}", file=sys.stderr)
            summaries[rho] = _MISSING_BUILDINPUT_SUMMARY
            continue
        try:
            summaries[rho] = _parse_buildinput_summary(proc.stdout)
        except ValueError as e:
            print(f"run_pilot: could not parse buildinput's output for rho={rho}: {e}", file=sys.stderr)
            summaries[rho] = _MISSING_BUILDINPUT_SUMMARY

    runpilot_proc = run_subprocess(
        [
            "go",
            "run",
            "./cmd/runpilot",
            "--nodes",
            args.nodes,
            "--pods",
            args.pods,
            "--seed",
            str(args.seed),
            "--ratio",
            str(args.ratio),
            "--rhos",
            ",".join(str(r) for r in _PILOT_RHOS),
            "--iterations",
            "1",
            "--out",
            str(records_dir),
        ]
    )
    if runpilot_proc.returncode != 0:
        print(f"run_pilot: runpilot failed:\n{runpilot_proc.stderr}", file=sys.stderr)

    try:
        texts = aggregate.read_record_texts_from_folder(records_dir)
        parsed = [aggregate.parse_record(text) for _, text in texts]
        records = [r for r in parsed if not r.truncated and r.transitions]
    except (OSError, ValueError, KeyError) as e:
        print(f"run_pilot: failed to read records from {records_dir}: {e}", file=sys.stderr)
        records = []

    # aggregate.run's --distributed-scheduler must match a record's
    # scheduler_profile exactly (aggregate.py's build_rho_sections compares
    # it to section.scheduler_order by equality, not containment); its own
    # default is the bare "LeastAllocated", but cmd/runpilot's header carries
    # schedulerprofile.Pilot's full description. Without this, scenario 6's
    # ρ = 1.1 prediction section always reports the distributed scheduler as
    # "not found" — found running against real openb data (units/U05.md's
    # "先に抜き出す割合を小さくした入力で1回通す" step).
    agg_exit = aggregate.run(
        ["--records-dir", str(records_dir), "--output-dir", str(report_dir), "--distributed-scheduler", _LEAST_ALLOCATED_DESCRIPTION]
    )
    if agg_exit != 0:
        print(f"run_pilot: aggregate report command failed (exit code {agg_exit})", file=sys.stderr)

    report_path = report_dir / "report.md"
    report_text = report_path.read_text(encoding="utf-8") if report_path.exists() else ""
    figures_dir = report_dir / "figures"
    existing_figures = {f"figures/{p.name}" for p in figures_dir.glob("*.png")} if figures_dir.exists() else set()

    results = run_all_checks(
        buildinput_summary_low_rho=summaries[aggregate.RHO_NEGATIVE_CONTROL],
        buildinput_summary_high_rho=summaries[aggregate.RHO_HIGH_LOAD],
        eligible_pods=eligible_pods,
        ratio=args.ratio,
        records=records,
        report_text=report_text,
        existing_figures=existing_figures,
    )

    print(_format_results(results))
    failed = [r for r in results if not r.passed]
    print(f"\n{len(results) - len(failed)}/{len(results)} checks passed")
    return 1 if failed else 0


def main() -> None:
    sys.exit(run(sys.argv[1:]))


if __name__ == "__main__":
    main()

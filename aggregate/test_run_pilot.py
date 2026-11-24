# /// script
# requires-python = ">=3.12"
# dependencies = [
#   "numpy",
#   "pandas",
#   "matplotlib",
# ]
# ///

"""Unit and integration tests for run_pilot.py (units/U05.md).

Run with: uv run --with pytest aggregate/test_run_pilot.py
Integration tests (marked `integration`, slow: each runs "go run" two or
three times) are excluded by default — "-m not integration" is always
prepended, so run_checks.sh's PY: line (which invokes this script with no
extra arguments at all) and a human adding "-q" or "-k ..." both still skip
them. Pass your own "-m" to override it, since pytest keeps the *last* -m
it sees: `... aggregate/test_run_pilot.py -m integration` runs only the
integration test, and `-m ""` runs everything.

Seams (rihib:interface-judge round 1): `run_all_checks` (the "検査とThenを
確かめる処理", a deep module) and `run` (the one outer entry point) are the
two public interfaces tests call. The individual `check_scenario*`/
`check_sanity_*`/`format_results`/`parse_buildinput_summary`/
`count_eligible_pods`/`expected_sampled_count` functions are implementation
detail of `run_all_checks` and are exercised only through it below, never
called directly.
"""

from __future__ import annotations

import json
import sys
from dataclasses import replace
from pathlib import Path
from types import SimpleNamespace

import pytest

sys.path.insert(0, str(Path(__file__).parent))
import aggregate
import run_pilot

POD_CSV_HEADER = "name,cpu_milli,memory_mib,num_gpu,gpu_milli,gpu_spec,qos,pod_phase,creation_time,deletion_time,scheduled_time\n"


def _transition(time: str, seq: int, operation: str, component_id: str, state: str = "", **kw) -> aggregate.Transition:
    return aggregate.Transition(
        time=time,
        seq=seq,
        operation=operation,
        component_id=component_id,
        task_id=kw.get("task_id"),
        state=state,
        state_key=kw.get("state_key"),
        before=kw.get("before"),
        after=kw.get("after"),
        task_immutable=kw.get("task_immutable"),
    )


def _header(**overrides) -> aggregate.RunHeader:
    fields = {
        "evaluation_id": "eval-1",
        "scenario_id": "rho1p1-leastallocated",
        "iteration": 0,
        "trace_name": "openb",
        "trace_version": "openb_pod_list_default",
        "reference_commit": "abc123",
        "input_seed": "1",
        "rho": 1.1,
        "evaluator_commit": "def456",
        "hardware": "test-machine",
        "scheduler_profile": "LeastAllocated",
    }
    fields.update(overrides)
    return aggregate.RunHeader(**fields)


def _create(time_sec: int, seq: int, task_id: str, instance_id: str) -> aggregate.Transition:
    return _transition(
        f"2024-01-01T00:00:{time_sec:02d}.000000000Z",
        seq,
        "create_task",
        instance_id,
        state="instance_status",
        task_id=task_id,
        after="pending",
    )


def _complete(time_sec: int, seq: int, task_id: str, instance_id: str) -> aggregate.Transition:
    return _transition(
        f"2024-01-01T00:00:{time_sec:02d}.000000000Z",
        seq,
        "complete",
        instance_id,
        state="instance_status",
        task_id=task_id,
        after="completed",
    )


class _Builder:
    """Builds a transition list with the same shape record.go's Manager writes
    (mirrors aggregate/test_aggregate.py's RecordBuilder), so the sanity-check
    tests below exercise run_all_checks's metrics computation the way a real
    record would, instead of constructing aggregate.Metrics directly."""

    def __init__(self) -> None:
        self._seq = 0
        self.transitions: list[aggregate.Transition] = []

    def _next_seq(self) -> int:
        self._seq += 1
        return self._seq

    def _write(self, time, op, component_id, task_id="", state="", state_key="", before=None, after=None, task_immutable=None) -> None:
        self.transitions.append(
            aggregate.Transition(
                time=f"2024-01-01T00:00:{time:02d}.000000000Z",
                seq=self._next_seq(),
                operation=op,
                component_id=component_id,
                task_id=task_id or None,
                state=state,
                state_key=state_key or None,
                before=before,
                after=after,
                task_immutable=task_immutable,
            )
        )

    def register_node(self, time, node_id, capacity: dict[str, str]) -> _Builder:
        self._write(time, "register_node", node_id, state="resource_capacity", after=capacity)
        return self

    def create_task(self, time, task_id, instance_id, cpu="1", memory="0", gpu="0") -> _Builder:
        immutable = {
            "owner": "u1",
            "group": "g1",
            "job_id": task_id,
            "resource_request": {"cpu": cpu, "memory": memory, "nvidia.com/gpu": gpu},
            "resource_limit": {"cpu": cpu, "memory": memory, "nvidia.com/gpu": gpu},
            "priority": 0,
        }
        self._write(time, "create_task", instance_id, task_id=task_id, state="instance_status", after="pending", task_immutable=immutable)
        self._write(time, "create_task", instance_id, task_id=task_id, state="assigned_node", after=None)
        return self

    def bind(self, time, task_id, instance_id, node_id, allocated: dict[str, str]) -> _Builder:
        self._write(time, "assign_node", instance_id, task_id=task_id, state="assigned_node", after=node_id)
        self._write(time, "assign_node", node_id, task_id=task_id, state="allocated_resource", state_key=instance_id, after=allocated)
        self._write(time, "start", instance_id, task_id=task_id, state="instance_status", before="pending", after="running")
        return self

    def complete(self, time, task_id, instance_id, node_id, allocated: dict[str, str]) -> _Builder:
        self._write(time, "complete", instance_id, task_id=task_id, state="instance_status", before="running", after="completed")
        self._write(time, "release", node_id, task_id=task_id, state="allocated_resource", state_key=instance_id, before=allocated, after=None)
        return self


def _find_record(records: list[aggregate.RunRecord], rho: float, scheduler_profile: str) -> aggregate.RunRecord:
    return next(r for r in records if r.header.rho == rho and r.header.scheduler_profile == scheduler_profile)


def _good_combo_record(rho: float, scheduler_profile: str) -> aggregate.RunRecord:
    """A minimal record good enough to pass every per-record check: one pod
    created and completed, one node whose capacity is recorded up front."""
    return aggregate.RunRecord(
        header=_header(rho=rho, scheduler_profile=scheduler_profile),
        transitions=[
            _transition("2024-01-01T00:00:00.000000000Z", 1, "register_node", "node-1", state="resource_capacity", after={"cpu": "10"}),
            _create(0, 2, "task-1", "instance-1"),
            _transition(
                "2024-01-01T00:00:05.000000000Z",
                3,
                "start",
                "instance-1",
                state="instance_status",
                task_id="task-1",
                before="pending",
                after="running",
            ),
            _complete(10, 4, "task-1", "instance-1"),
        ],
    )


GOOD_REPORT_TEXT = """# 実験1 集計報告

## ρ = 0.5

### 実行ごとのメトリクス

| scheduler | iteration | pending_count_at_end | cluster_std | nonzero_wait_fraction | allocation_rate:cpu | unplaceable_free:cpu |
| --- | --- | --- | --- | --- | --- | --- |
| LeastAllocated | 0 | 0 | 0.5 | 0.1 | 0.3 | 0.0 |

### 待機時間の分布

![pending wait time distribution for ρ = 0.5](figures/rho-0.5-wait.png)

### スケジューラの差

LeastAllocated − MostAllocated

| metric | diff |
| --- | --- |
| cluster_std | 0.1 |

### 陰性対照

待機時間が0でないPodの割合:

- LeastAllocated: 0.1000
- MostAllocated: 0.1200

待機時間の差:

- wait:0: 0.3000

## ρ = 1.1

### 実行ごとのメトリクス

| scheduler | iteration | pending_count_at_end | cluster_std | nonzero_wait_fraction | allocation_rate:cpu | unplaceable_free:cpu |
| --- | --- | --- | --- | --- | --- | --- |
| LeastAllocated | 0 | 0 | 0.5 | 0.1 | 0.3 | 0.0 |

### 待機時間の分布

![pending wait time distribution for ρ = 1.1](figures/rho-1.1-wait.png)

### スケジューラの差

LeastAllocated − MostAllocated

| metric | diff |
| --- | --- |
| cluster_std | 0.2 |

### 予測と結果（高負荷）

予測（experiment.md 3.1）: LeastAllocated の方が、大きな要求を持つタスクの待機時間と、置けない空き資源の量が大きい。

| metric | diff（LeastAllocated − 他） | 予測が当たったか |
| --- | --- | --- |
| unplaceable_free:cpu | 0.4000 | 当たった |
| wait:8 | 0.5000 | 当たった |
"""


def _good_run_all_checks_kwargs() -> dict:
    return {
        "buildinput_summary_low_rho": run_pilot.BuildInputSummary(used_pods=1, excluded_pods=0, failed_pods=0, rho_before=0.3, rho_after=0.5),
        "buildinput_summary_high_rho": run_pilot.BuildInputSummary(used_pods=1, excluded_pods=0, failed_pods=0, rho_before=0.3, rho_after=1.1),
        "eligible_pods": 1,
        "ratio": 1.0,
        "records": [
            _good_combo_record(0.5, "LeastAllocated"),
            _good_combo_record(0.5, "MostAllocated"),
            _good_combo_record(1.1, "LeastAllocated"),
            _good_combo_record(1.1, "MostAllocated"),
        ],
        "report_text": GOOD_REPORT_TEXT,
        "existing_figures": {"figures/rho-0.5-wait.png", "figures/rho-1.1-wait.png"},
    }


def _fully_passing_record(rho: float, scheduler_profile: str, running_per_node: dict[str, int], cpu_allocated: str) -> aggregate.RunRecord:
    """Builds a record crafted to pass every per-record and per-combination
    check at once: running_per_node's pods are created, bound, and completed
    well before the observation ends (so cluster_std and allocation_rate are
    both measured over a real interval, and every pod's final status is
    "completed", not left "running" the way _good_combo_record's sibling
    helpers leave theirs for simpler, single-purpose fixtures)."""
    node_per_pod = [node_id for node_id, count in running_per_node.items() for _ in range(count)]
    pods = [(f"task-{i}", f"instance-{i}", node_id) for i, node_id in enumerate(node_per_pod, start=1)]
    # Written one *phase* at a time (every create_task at t=0, then every
    # bind at t=5, then every complete at t=8), not pod by pod: writing a
    # whole pod's create/bind/complete before moving to the next pod would
    # make the raw transition list jump from t=8 (one pod's complete) back
    # to t=0 (the next pod's create), breaking check_scenario2's "transitions
    # carry non-decreasing virtual time" — found writing this fixture.
    b = _Builder()
    for node_id in running_per_node:
        b.register_node(0, node_id, {"cpu": "100"})
    for task_id, instance_id, _ in pods:
        b.create_task(0, task_id, instance_id, cpu=cpu_allocated)
    for task_id, instance_id, node_id in pods:
        b.bind(5, task_id, instance_id, node_id, {"cpu": cpu_allocated})
    for task_id, instance_id, node_id in pods:
        b.complete(8, task_id, instance_id, node_id, {"cpu": cpu_allocated})
    b.create_task(10, "task-end", "instance-end", cpu="0")  # stays pending; marks the end of observation
    return aggregate.RunRecord(header=_header(rho=rho, scheduler_profile=scheduler_profile), transitions=b.transitions)


def _fully_passing_run_all_checks_kwargs() -> dict:
    """A fixture built to make every single check in run_all_checks pass, not
    just run without "material missing" (rihib:interface-judge round 2):
    each (rho, scheduler) combination creates the same 3 pods (2 running + 1
    left pending, matching eligible_pods=3 x ratio=1.0 below), so sanity
    counts match everywhere; LeastAllocated always spreads its 2 running pods
    one-per-node while MostAllocated always stacks both on one node, so
    cluster_std(LeastAllocated) < cluster_std(MostAllocated) at both rhos;
    and every scheduler's allocated cpu per pod is higher at ρ = 1.1 than at
    ρ = 0.5, so allocation_rate increases with rho for both schedulers."""
    return {
        "buildinput_summary_low_rho": run_pilot.BuildInputSummary(used_pods=3, excluded_pods=0, failed_pods=0, rho_before=0.3, rho_after=0.5),
        "buildinput_summary_high_rho": run_pilot.BuildInputSummary(used_pods=3, excluded_pods=0, failed_pods=0, rho_before=0.3, rho_after=1.1),
        "eligible_pods": 3,
        "ratio": 1.0,
        "records": [
            _fully_passing_record(0.5, "LeastAllocated", {"node-1": 1, "node-2": 1}, cpu_allocated="1"),
            _fully_passing_record(0.5, "MostAllocated", {"node-1": 2, "node-2": 0}, cpu_allocated="1"),
            _fully_passing_record(1.1, "LeastAllocated", {"node-1": 1, "node-2": 1}, cpu_allocated="4"),
            _fully_passing_record(1.1, "MostAllocated", {"node-1": 2, "node-2": 0}, cpu_allocated="4"),
        ],
        "report_text": GOOD_REPORT_TEXT,
        "existing_figures": {"figures/rho-0.5-wait.png", "figures/rho-1.1-wait.png"},
    }


def test_run_all_checks_returns_only_passing_results_for_well_formed_inputs() -> None:
    results = run_pilot.run_all_checks(**_fully_passing_run_all_checks_kwargs())

    assert len(results) > 0
    failed = [r for r in results if not r.passed]
    assert all(r.passed for r in results), failed


def test_run_all_checks_reports_scenario2_material_missing_when_the_rho_1_1_least_allocated_record_is_absent() -> None:
    kwargs = _good_run_all_checks_kwargs()
    kwargs["records"] = [r for r in kwargs["records"] if not (r.header.rho == 1.1 and r.header.scheduler_profile == "LeastAllocated")]

    results = run_pilot.run_all_checks(**kwargs)

    # Every check that needed the missing record is reported as failed with
    # "material missing" in its detail (units/U05.md: "前の段が落ちて後の項目
    # の材料ができないときは、後の項目を「材料が無い」として落とす"), instead
    # of raising or silently skipping. Four distinct checks need this one
    # record: the sanity record-exists check itself, scenario 2 (which only
    # ever looks at the ρ=1.1/LeastAllocated record), the ρ=1.1/LeastAllocated
    # side of the rho-effect comparison, and the ρ=1.1 side of the
    # scheduler-effect comparison — each must be named and asserted
    # individually, since a single assertion could pass by matching only one
    # of the four while the other three silently raised or were skipped.
    missing_names = [
        "sanity: record for rho=1.1, LeastAllocated exists",
        "scenario2: record for rho=1.1, LeastAllocated exists",
        "sanity: rho effect on allocation_rate (LeastAllocated)",
        "sanity: scheduler effect on cluster_std (rho=1.1)",
    ]
    for name in missing_names:
        check = next(r for r in results if r.name == name)
        assert check.passed is False
        assert "material missing" in check.detail


def test_run_all_checks_matches_records_whose_scheduler_profile_is_the_full_description() -> None:
    # scenarios/schedulerprofile.Pilot's description is not the bare string
    # "LeastAllocated"/"MostAllocated" cmd/runpilot's own records.go header
    # carries (schedulerprofile_test.go, cmd/runpilot's integration run
    # confirmed with the real pilot: "NodeResourcesFit(scoringStrategy=
    # LeastAllocated, resources=cpu:1,memory:1,nvidia.com/gpu:1);
    # NodeResourcesBalancedAllocation disabled"). run_all_checks must still
    # find each (rho, scheduler) record by that real header value, not only
    # by the bare name _good_combo_record's fixture happens to use.
    kwargs = _good_run_all_checks_kwargs()
    kwargs["records"] = [
        aggregate.RunRecord(
            header=_header(
                rho=r.header.rho,
                scheduler_profile=(
                    f"NodeResourcesFit(scoringStrategy={r.header.scheduler_profile}, resources=cpu:1,memory:1,nvidia.com/gpu:1); "
                    "NodeResourcesBalancedAllocation disabled"
                ),
            ),
            transitions=r.transitions,
        )
        for r in kwargs["records"]
    ]

    results = run_pilot.run_all_checks(**kwargs)

    assert all("material missing" not in r.detail for r in results)


# --- scenario 1: input summary ---


def test_run_all_checks_flags_scenario1_when_used_pod_count_is_off() -> None:
    kwargs = _good_run_all_checks_kwargs()
    kwargs["buildinput_summary_high_rho"] = replace(kwargs["buildinput_summary_high_rho"], used_pods=999)

    results = run_pilot.run_all_checks(**kwargs)

    check = next(r for r in results if r.name == "scenario1: used pods is 80% of non-pending pods")
    assert check.passed is False


def test_run_all_checks_flags_scenario1_when_rho_does_not_match_target() -> None:
    kwargs = _good_run_all_checks_kwargs()
    kwargs["buildinput_summary_high_rho"] = replace(kwargs["buildinput_summary_high_rho"], rho_after=1.05)

    results = run_pilot.run_all_checks(**kwargs)

    check = next(r for r in results if r.name == "scenario1: achieved rho matches the target")
    assert check.passed is False


def test_run_all_checks_flags_rho_achieved_when_the_low_rho_summary_does_not_match_its_target() -> None:
    kwargs = _good_run_all_checks_kwargs()
    kwargs["buildinput_summary_low_rho"] = replace(kwargs["buildinput_summary_low_rho"], rho_after=0.45)

    results = run_pilot.run_all_checks(**kwargs)

    check = next(r for r in results if r.name == f"sanity: rho={aggregate.RHO_NEGATIVE_CONTROL} achieved matches the target")
    assert check.passed is False


# --- scenario 2: one run's record ---


def test_run_all_checks_flags_scenario2_header_when_a_commit_is_missing() -> None:
    kwargs = _good_run_all_checks_kwargs()
    records = kwargs["records"]
    target = _find_record(records, 1.1, "LeastAllocated")
    broken = aggregate.RunRecord(header=replace(target.header, reference_commit=""), transitions=target.transitions)
    kwargs["records"] = [r if r is not target else broken for r in records]

    results = run_pilot.run_all_checks(**kwargs)

    check = next(r for r in results if "header names" in r.name)
    assert check.passed is False


def test_run_all_checks_flags_scenario2_capacity_when_no_node_is_registered() -> None:
    kwargs = _good_run_all_checks_kwargs()
    records = kwargs["records"]
    target = _find_record(records, 1.1, "LeastAllocated")
    without_capacity = aggregate.RunRecord(
        header=target.header,
        transitions=[t for t in target.transitions if t.operation != "register_node"],
    )
    kwargs["records"] = [r if r is not target else without_capacity for r in records]

    results = run_pilot.run_all_checks(**kwargs)

    check = next(r for r in results if "capacity" in r.name)
    assert check.passed is False


def test_run_all_checks_flags_scenario2_time_order_when_transitions_are_out_of_order() -> None:
    kwargs = _good_run_all_checks_kwargs()
    records = kwargs["records"]
    target = _find_record(records, 1.1, "LeastAllocated")
    t = target.transitions
    reordered = aggregate.RunRecord(header=target.header, transitions=[t[0], t[-1], *t[1:-1]])  # complete before start, by position
    kwargs["records"] = [r if r is not target else reordered for r in records]

    results = run_pilot.run_all_checks(**kwargs)

    check = next(r for r in results if "virtual time" in r.name)
    assert check.passed is False


def test_run_all_checks_flags_scenario2_when_no_pod_was_ever_assigned_or_completed() -> None:
    # units/U05.md scenario 2 asks that 配置と完了の遷移 carry a virtual time,
    # which the time-order check alone cannot show: a record where nothing was
    # ever placed has no such transition to order, so it passed vacuously
    # (round1 review, correctness Must 2).
    kwargs = _good_run_all_checks_kwargs()
    records = kwargs["records"]
    target = _find_record(records, 1.1, "LeastAllocated")
    b = _Builder()
    b.register_node(0, "node-1", {"cpu": "10"})
    b.create_task(0, "task-1", "instance-1")  # created, but never bound and never completed
    never_placed = aggregate.RunRecord(header=target.header, transitions=b.transitions)
    kwargs["records"] = [r if r is not target else never_placed for r in records]

    results = run_pilot.run_all_checks(**kwargs)

    check = next(r for r in results if r.name == "scenario2: assign and complete transitions are recorded with a virtual time")
    assert check.passed is False


# --- scenario 3: four records ---


def test_run_all_checks_flags_scenario3_when_a_combination_is_missing() -> None:
    kwargs = _good_run_all_checks_kwargs()
    kwargs["records"] = [r for r in kwargs["records"] if not (r.header.rho == 1.1 and r.header.scheduler_profile == "MostAllocated")]

    results = run_pilot.run_all_checks(**kwargs)

    check = next(r for r in results if r.name == "scenario3: four records exist")
    assert check.passed is False


def test_run_all_checks_flags_scenario3_when_two_records_share_a_combination() -> None:
    kwargs = _good_run_all_checks_kwargs()
    kwargs["records"] = [
        _good_combo_record(0.5, "LeastAllocated"),
        _good_combo_record(0.5, "LeastAllocated"),  # duplicate combination, so only 3 are distinct
        _good_combo_record(1.1, "LeastAllocated"),
        _good_combo_record(1.1, "MostAllocated"),
    ]

    results = run_pilot.run_all_checks(**kwargs)

    check = next(r for r in results if r.name == "scenario3: the four records' (rho, scheduler) combinations are all distinct")
    assert check.passed is False


# --- scenario 4: report's per-run table, diff, figures ---


def test_run_all_checks_flags_scenario4_when_a_referenced_figure_is_missing() -> None:
    kwargs = _good_run_all_checks_kwargs()
    kwargs["existing_figures"] = {"figures/rho-0.5-wait.png"}  # rho-1.1-wait.png missing

    results = run_pilot.run_all_checks(**kwargs)

    check = next(r for r in results if r.name == "scenario4: every figure the report links to exists")
    assert check.passed is False


def test_run_all_checks_flags_scenario4_when_the_diff_is_only_the_fallback_message() -> None:
    kwargs = _good_run_all_checks_kwargs()
    kwargs["report_text"] = GOOD_REPORT_TEXT.replace(
        "LeastAllocated − MostAllocated\n\n| metric | diff |\n| --- | --- |\n| cluster_std | 0.2 |",
        "LeastAllocated と MostAllocated がどの入力の版でもそろわず、差を計算できなかった。",
    )

    results = run_pilot.run_all_checks(**kwargs)

    check = next(r for r in results if r.name == "scenario4: every rho section has a real scheduler diff")
    assert check.passed is False


def test_run_all_checks_flags_scenario4_when_the_per_run_table_is_missing() -> None:
    kwargs = _good_run_all_checks_kwargs()
    # GOOD_REPORT_TEXT's two rho sections share the exact same per-run table
    # text; replace(..., 1) drops only the first occurrence (ρ = 0.5's),
    # leaving ρ = 1.1's intact, so only one of the two rho sections is broken.
    kwargs["report_text"] = GOOD_REPORT_TEXT.replace(
        "### 実行ごとのメトリクス\n\n"
        "| scheduler | iteration | pending_count_at_end | cluster_std | nonzero_wait_fraction | allocation_rate:cpu | unplaceable_free:cpu |\n"
        "| --- | --- | --- | --- | --- | --- | --- |\n"
        "| LeastAllocated | 0 | 0 | 0.5 | 0.1 | 0.3 | 0.0 |\n\n",
        "",
        1,
    )

    results = run_pilot.run_all_checks(**kwargs)

    check = next(r for r in results if r.name == "scenario4: every rho section has the five-metric per-run table")
    assert check.passed is False


def test_run_all_checks_flags_scenario4_when_the_per_run_table_has_no_wait_time_column() -> None:
    # units/U05.md scenario 4 names five metrics the per-run table must carry,
    # and クラスごとの待機時間 is one of them: aggregate.py's
    # _render_per_run_table writes it as the nonzero_wait_fraction column, so
    # dropping that column must fail this check (round1 review, correctness
    # Must 1: it was the one metric of the five nothing checked).
    kwargs = _good_run_all_checks_kwargs()
    # The two rho sections' header rows are identical text; count=1 drops only
    # ρ = 0.5's, leaving ρ = 1.1's table intact.
    kwargs["report_text"] = GOOD_REPORT_TEXT.replace("nonzero_wait_fraction | ", "", 1)

    results = run_pilot.run_all_checks(**kwargs)

    check = next(r for r in results if r.name == "scenario4: every rho section has the five-metric per-run table")
    assert check.passed is False


def test_run_all_checks_flags_scenario4_when_a_rho_section_links_to_no_figure() -> None:
    # The other half of units/U05.md scenario 4's クラスごとの待機時間: the
    # per-class wait times appear only in the distribution figure, so a rho
    # section linking to no figure at all must fail, instead of passing
    # vacuously because "every referenced figure exists" is trivially true
    # for zero references (round1 review, correctness Must 1 and IMO 7).
    kwargs = _good_run_all_checks_kwargs()
    kwargs["report_text"] = GOOD_REPORT_TEXT.replace("![pending wait time distribution for ρ = 1.1](figures/rho-1.1-wait.png)\n", "")

    results = run_pilot.run_all_checks(**kwargs)

    check = next(r for r in results if r.name == "scenario4: every rho section links to a wait-time distribution figure")
    assert check.passed is False


# --- scenario 5: negative control (ρ = 0.5) ---


def test_run_all_checks_flags_scenario5_when_rho_0_5_has_no_negative_control_section() -> None:
    kwargs = _good_run_all_checks_kwargs()
    kwargs["report_text"] = GOOD_REPORT_TEXT.split("### 陰性対照")[0] + "## ρ = 1.1\n"

    results = run_pilot.run_all_checks(**kwargs)

    check = next(r for r in results if r.name == "scenario5: rho = 0.5 has a negative-control section")
    assert check.passed is False


def test_run_all_checks_flags_scenario5_when_fewer_than_two_schedulers_are_listed() -> None:
    kwargs = _good_run_all_checks_kwargs()
    kwargs["report_text"] = GOOD_REPORT_TEXT.replace("- MostAllocated: 0.1200\n", "")

    results = run_pilot.run_all_checks(**kwargs)

    check = next(r for r in results if r.name == "scenario5: at least two schedulers' nonzero-wait fraction is shown")
    assert check.passed is False


def test_run_all_checks_flags_scenario5_when_the_wait_time_diff_is_missing() -> None:
    kwargs = _good_run_all_checks_kwargs()
    kwargs["report_text"] = GOOD_REPORT_TEXT.replace("待機時間の差:\n\n- wait:0: 0.3000\n\n", "")

    results = run_pilot.run_all_checks(**kwargs)

    check = next(r for r in results if r.name == "scenario5: the wait-time diff between schedulers is shown")
    assert check.passed is False


def test_run_all_checks_counts_a_scheduler_name_containing_spaces_in_the_negative_control() -> None:
    # aggregate.py's render_rho_markdown writes the real scheduler_profile
    # value verbatim (schedulerprofile.Pilot's description, which has spaces
    # and commas), not the bare "LeastAllocated"/"MostAllocated" GOOD_REPORT_TEXT
    # otherwise uses. Found running against real openb data (units/U05.md's
    # "先に抜き出す割合を小さくした入力で1回通す" step).
    kwargs = _good_run_all_checks_kwargs()
    kwargs["report_text"] = GOOD_REPORT_TEXT.replace(
        "- LeastAllocated: 0.1000\n- MostAllocated: 0.1200\n",
        "- NodeResourcesFit(scoringStrategy=LeastAllocated, resources=cpu:1,memory:1,nvidia.com/gpu:1); NodeResourcesBalancedAllocation disabled: 0.1000\n"
        "- NodeResourcesFit(scoringStrategy=MostAllocated, resources=cpu:1,memory:1,nvidia.com/gpu:1); NodeResourcesBalancedAllocation disabled: 0.1200\n",
    )

    results = run_pilot.run_all_checks(**kwargs)

    check = next(r for r in results if r.name == "scenario5: at least two schedulers' nonzero-wait fraction is shown")
    assert check.passed is True


# --- scenario 6: prediction and result (ρ = 1.1) ---


def test_run_all_checks_flags_scenario6_when_rho_1_1_has_no_prediction_section() -> None:
    kwargs = _good_run_all_checks_kwargs()
    kwargs["report_text"] = GOOD_REPORT_TEXT.split("### 予測と結果（高負荷）")[0]

    results = run_pilot.run_all_checks(**kwargs)

    check = next(r for r in results if r.name == "scenario6: rho = 1.1 has a prediction-and-result section")
    assert check.passed is False


def test_run_all_checks_flags_scenario6_when_the_unplaceable_free_row_is_missing() -> None:
    kwargs = _good_run_all_checks_kwargs()
    kwargs["report_text"] = GOOD_REPORT_TEXT.replace("| unplaceable_free:cpu | 0.4000 | 当たった |\n", "")

    results = run_pilot.run_all_checks(**kwargs)

    check = next(r for r in results if r.name == "scenario6: an unplaceable-free prediction/result row is shown")
    assert check.passed is False


def test_run_all_checks_flags_scenario6_when_the_wait_row_is_missing() -> None:
    kwargs = _good_run_all_checks_kwargs()
    kwargs["report_text"] = GOOD_REPORT_TEXT.replace("| wait:8 | 0.5000 | 当たった |\n", "")

    results = run_pilot.run_all_checks(**kwargs)

    check = next(r for r in results if r.name == "scenario6: a large-request wait-time prediction/result row is shown")
    assert check.passed is False


# --- sanity: counts ---


def test_run_all_checks_flags_sanity_counts_when_created_count_does_not_match() -> None:
    kwargs = _good_run_all_checks_kwargs()
    records = kwargs["records"]
    target = _find_record(records, 1.1, "LeastAllocated")
    truncated = aggregate.RunRecord(header=target.header, transitions=[])  # no create_task at all
    kwargs["records"] = [r if r is not target else truncated for r in records]

    results = run_pilot.run_all_checks(**kwargs)

    check = next(r for r in results if r.name == "sanity: created pod count matches the expected used-pod count (rho=1.1, LeastAllocated)")
    assert check.passed is False


def test_run_all_checks_flags_sanity_counts_when_a_pod_never_finishes() -> None:
    kwargs = _good_run_all_checks_kwargs()
    records = kwargs["records"]
    target = _find_record(records, 1.1, "LeastAllocated")
    b = _Builder()
    b.register_node(0, "node-1", {"cpu": "10"})
    b.create_task(0, "task-1", "instance-1")
    b.bind(5, "task-1", "instance-1", "node-1", {"cpu": "1"})  # never completes
    stuck = aggregate.RunRecord(header=target.header, transitions=b.transitions)
    kwargs["records"] = [r if r is not target else stuck for r in records]

    results = run_pilot.run_all_checks(**kwargs)

    check = next(r for r in results if r.name == "sanity: every pod's final status is completed or pending (rho=1.1, LeastAllocated)")
    assert check.passed is False


# --- sanity: ranges ---
#
# Only ranges reachable through compute_metrics from *some* transition list
# are exercised here. A negative pending wait time and a negative
# unplaceable_free are not: compute_metrics sorts transitions by time before
# replaying them, so a wait can never go negative, and
# _is_fragmentation_blocked's "fits_aggregate" gate (free_sum >= a positive
# request) can never fire while free_sum is negative, so a negative
# unplaceable_free is never recorded either. Both checks still run (and pass)
# on every well-formed record above; their failure branches are defensive
# code with no reachable input, which this seam cannot exercise honestly.


def test_run_all_checks_flags_sanity_ranges_when_allocation_rate_exceeds_one() -> None:
    kwargs = _good_run_all_checks_kwargs()
    records = kwargs["records"]
    target = _find_record(records, 1.1, "LeastAllocated")
    b = _Builder()
    b.register_node(0, "node-1", {"cpu": "1"})  # tiny capacity
    b.create_task(0, "task-1", "instance-1", cpu="5")
    b.bind(5, "task-1", "instance-1", "node-1", {"cpu": "5"})  # over-allocated: 5 > capacity 1
    b.create_task(10, "task-2", "instance-2", cpu="0")  # marks the end of observation
    overallocated = aggregate.RunRecord(header=target.header, transitions=b.transitions)
    kwargs["records"] = [r if r is not target else overallocated for r in records]

    results = run_pilot.run_all_checks(**kwargs)

    check = next(r for r in results if r.name == "sanity: allocation_rate is within [0, 1] (rho=1.1, LeastAllocated)")
    assert check.passed is False


def test_run_all_checks_flags_sanity_ranges_when_a_metric_is_nan() -> None:
    kwargs = _good_run_all_checks_kwargs()
    records = kwargs["records"]
    target = _find_record(records, 1.1, "LeastAllocated")
    # A malformed allocated-resource quantity ("NaN", rather than a parseable
    # one) is bad data a corrupted record could genuinely carry, unlike a
    # negative wait or a negative unplaceable_free (see the module comment
    # above): parse_quantity's fallback is float(value), which accepts "NaN".
    # Lowercase "nan" will not do: it ends with "n" (parse_quantity's own
    # nano-suffix letter), so it is parsed as float("na") * 1e-9 and raises
    # instead of producing a NaN.
    transitions = [
        _transition("2024-01-01T00:00:00.000000000Z", 1, "register_node", "node-1", state="resource_capacity", after={"cpu": "10"}),
        _create(0, 2, "task-1", "instance-1"),
        _transition("2024-01-01T00:00:05.000000000Z", 3, "assign_node", "instance-1", state="assigned_node", task_id="task-1", after="node-1"),
        _transition(
            "2024-01-01T00:00:05.000000000Z",
            4,
            "assign_node",
            "node-1",
            state="allocated_resource",
            task_id="task-1",
            state_key="instance-1",
            after={"cpu": "NaN"},
        ),
        _transition(
            "2024-01-01T00:00:05.000000000Z",
            5,
            "start",
            "instance-1",
            state="instance_status",
            task_id="task-1",
            before="pending",
            after="running",
        ),
        _create(10, 6, "task-2", "instance-2"),  # marks the end of observation
    ]
    broken = aggregate.RunRecord(header=target.header, transitions=transitions)
    kwargs["records"] = [r if r is not target else broken for r in records]

    results = run_pilot.run_all_checks(**kwargs)

    check = next(r for r in results if r.name == "sanity: no metric is NaN (rho=1.1, LeastAllocated)")
    assert check.passed is False


# --- sanity: rho effect on allocation_rate ---


def _single_resource_record(rho: float, scheduler_profile: str, resource: str, capacity: str, allocated: str) -> aggregate.RunRecord:
    b = _Builder()
    b.register_node(0, "node-1", {resource: capacity})
    b.create_task(0, "task-1", "instance-1")
    b.bind(5, "task-1", "instance-1", "node-1", {resource: allocated})
    b.create_task(10, "task-2", "instance-2")  # marks the end of observation
    return aggregate.RunRecord(header=_header(rho=rho, scheduler_profile=scheduler_profile), transitions=b.transitions)


def test_run_all_checks_flags_rho_effect_when_allocation_rate_does_not_increase() -> None:
    kwargs = _good_run_all_checks_kwargs()
    replaced = {
        (0.5, "LeastAllocated"): _single_resource_record(0.5, "LeastAllocated", "cpu", "10", "8"),  # 0.8
        (1.1, "LeastAllocated"): _single_resource_record(1.1, "LeastAllocated", "cpu", "10", "2"),  # 0.2: lower at the higher rho
    }
    kwargs["records"] = [replaced.get((r.header.rho, r.header.scheduler_profile), r) for r in kwargs["records"]]

    results = run_pilot.run_all_checks(**kwargs)

    check = next(r for r in results if r.name == "sanity: rho effect on allocation_rate (LeastAllocated)")
    assert check.passed is False


def test_run_all_checks_flags_rho_effect_when_no_resource_is_common() -> None:
    kwargs = _good_run_all_checks_kwargs()
    replaced = {
        (0.5, "LeastAllocated"): _single_resource_record(0.5, "LeastAllocated", "cpu", "10", "5"),
        (1.1, "LeastAllocated"): _single_resource_record(1.1, "LeastAllocated", "memory", "10", "5"),
    }
    kwargs["records"] = [replaced.get((r.header.rho, r.header.scheduler_profile), r) for r in kwargs["records"]]

    results = run_pilot.run_all_checks(**kwargs)

    check = next(r for r in results if r.name == "sanity: rho effect on allocation_rate (LeastAllocated)")
    assert check.passed is False


def test_run_all_checks_rho_effect_ignores_a_resource_never_allocated_by_either_run() -> None:
    # Found running against real openb data (units/U05.md's "先に抜き出す割合
    # を小さくした入力で1回通す" step): every node's capacity carries a "pods"
    # entry (scenarios/clusterdata/gpu2023.go's buildNode sets
    # corev1.ResourcePods), but nothing ever allocates against it, so its
    # allocation_rate is always exactly 0.0 regardless of rho. That resource
    # must not count as "did not increase" when the resources that are
    # actually exercised (here cpu) did increase as expected.
    def record(rho: float, cpu_allocated: str) -> aggregate.RunRecord:
        b = _Builder()
        b.register_node(0, "node-1", {"cpu": "10", "pods": "110"})
        b.create_task(0, "task-1", "instance-1", cpu=cpu_allocated)
        b.bind(5, "task-1", "instance-1", "node-1", {"cpu": cpu_allocated})
        b.create_task(10, "task-2", "instance-2", cpu="0")
        return aggregate.RunRecord(header=_header(rho=rho, scheduler_profile="LeastAllocated"), transitions=b.transitions)

    kwargs = _good_run_all_checks_kwargs()
    replaced = {(0.5, "LeastAllocated"): record(0.5, "3"), (1.1, "LeastAllocated"): record(1.1, "8")}
    kwargs["records"] = [replaced.get((r.header.rho, r.header.scheduler_profile), r) for r in kwargs["records"]]

    results = run_pilot.run_all_checks(**kwargs)

    check = next(r for r in results if r.name == "sanity: rho effect on allocation_rate (LeastAllocated)")
    assert check.passed is True


# --- sanity: scheduler effect on cluster_std ---


def _record_with_running_distribution(rho: float, scheduler_profile: str, running_per_node: dict[str, int]) -> aggregate.RunRecord:
    b = _Builder()
    for node_id in running_per_node:
        b.register_node(0, node_id, {"cpu": "100"})
    pod_i = 0
    for node_id, count in running_per_node.items():
        for _ in range(count):
            pod_i += 1
            task_id, instance_id = f"task-{pod_i}", f"instance-{pod_i}"
            b.create_task(0, task_id, instance_id, cpu="1")
            b.bind(5, task_id, instance_id, node_id, {"cpu": "1"})
    b.create_task(10, "task-end", "instance-end", cpu="0")  # marks the end of observation, stays pending
    return aggregate.RunRecord(header=_header(rho=rho, scheduler_profile=scheduler_profile), transitions=b.transitions)


def test_run_all_checks_flags_scheduler_effect_when_cluster_std_is_equal() -> None:
    kwargs = _good_run_all_checks_kwargs()
    replaced = {
        (0.5, "LeastAllocated"): _record_with_running_distribution(0.5, "LeastAllocated", {"node-1": 1, "node-2": 1}),
        (0.5, "MostAllocated"): _record_with_running_distribution(0.5, "MostAllocated", {"node-1": 1, "node-2": 1}),
    }
    kwargs["records"] = [replaced.get((r.header.rho, r.header.scheduler_profile), r) for r in kwargs["records"]]

    results = run_pilot.run_all_checks(**kwargs)

    check = next(r for r in results if r.name == f"sanity: scheduler effect on cluster_std (rho={aggregate.RHO_NEGATIVE_CONTROL})")
    assert check.passed is False


def test_run_all_checks_flags_scheduler_effect_when_least_allocated_has_the_bigger_std() -> None:
    kwargs = _good_run_all_checks_kwargs()
    replaced = {
        (0.5, "LeastAllocated"): _record_with_running_distribution(0.5, "LeastAllocated", {"node-1": 2, "node-2": 0}),  # skewed
        (0.5, "MostAllocated"): _record_with_running_distribution(0.5, "MostAllocated", {"node-1": 1, "node-2": 1}),  # even
    }
    kwargs["records"] = [replaced.get((r.header.rho, r.header.scheduler_profile), r) for r in kwargs["records"]]

    results = run_pilot.run_all_checks(**kwargs)

    check = next(r for r in results if r.name == f"sanity: scheduler effect on cluster_std (rho={aggregate.RHO_NEGATIVE_CONTROL})")
    assert check.passed is False


def test_run_all_checks_flags_scheduler_effect_when_cluster_std_is_not_measured() -> None:
    kwargs = _good_run_all_checks_kwargs()
    records = kwargs["records"]
    target = _find_record(records, 0.5, "LeastAllocated")
    b = _Builder()
    b.create_task(0, "task-1", "instance-1")  # no register_node at all: no interval ever has a node to measure
    unmeasured = aggregate.RunRecord(header=target.header, transitions=b.transitions)
    kwargs["records"] = [r if r is not target else unmeasured for r in records]

    results = run_pilot.run_all_checks(**kwargs)

    check = next(r for r in results if r.name == f"sanity: scheduler effect on cluster_std (rho={aggregate.RHO_NEGATIVE_CONTROL})")
    assert check.passed is False


# --- run (argv) ---


def _write_record(path: Path, record: aggregate.RunRecord) -> None:
    header = {
        "evaluation_id": record.header.evaluation_id,
        "scenario_id": record.header.scenario_id,
        "iteration": record.header.iteration,
        "input": {
            "trace_name": record.header.trace_name,
            "trace_version": record.header.trace_version,
            "reference_commit": record.header.reference_commit,
            "input_seed": record.header.input_seed,
            "rho": record.header.rho,
        },
        "execution": {
            "evaluator_commit": record.header.evaluator_commit,
            "reference_commit": record.header.reference_commit,
            "hardware": record.header.hardware,
        },
        "scheduler": {"profile": record.header.scheduler_profile},
    }
    lines = [json.dumps(header)]
    for t in record.transitions:
        lines.append(
            json.dumps(
                {
                    "time": t.time,
                    "seq": t.seq,
                    "operation": t.operation,
                    "component_id": t.component_id,
                    "task_id": t.task_id,
                    "state": t.state,
                    "state_key": t.state_key,
                    "before": t.before,
                    "after": t.after,
                    "task_immutable": t.task_immutable,
                }
            )
        )
    path.write_text("\n".join(lines) + "\n")


def _buildinput_stdout(used_pods: int, rho_after: float) -> str:
    return f"used pods:     {used_pods}\nexcluded pods: 0\nfailed pods:   0\nrho (before):  0.3\nrho (after):   {rho_after:.6f}\n"


_GOOD_BUILDINPUT_STDOUT = {"0.5": _buildinput_stdout(1, 0.5), "1.1": _buildinput_stdout(1, 1.1)}
_FULLY_PASSING_BUILDINPUT_STDOUT = {"0.5": _buildinput_stdout(3, 0.5), "1.1": _buildinput_stdout(3, 1.1)}


def _make_fake_subprocess(buildinput_stdout_by_rho: dict[str, str]):
    """Builds a fake for run()'s run_subprocess: answers "go run ./cmd/buildinput"
    with a canned summary (so run() does not need a real openb CSV to parse
    one from) and "go run ./cmd/runpilot" with success and no output, since
    the test places the records cmd/runpilot would have written itself."""

    def fake(args: list[str]) -> SimpleNamespace:
        if "./cmd/buildinput" in args:
            rho = args[args.index("--rho") + 1]
            return SimpleNamespace(returncode=0, stdout=buildinput_stdout_by_rho[rho], stderr="")
        if "./cmd/runpilot" in args:
            return SimpleNamespace(returncode=0, stdout="", stderr="")
        raise AssertionError(f"unexpected run_subprocess call: {args}")

    return fake


def test_run_prints_pass_and_fail_lines_and_returns_nonzero_when_some_checks_fail(tmp_path: Path, capsys: pytest.CaptureFixture[str]) -> None:
    out_dir = tmp_path / "out"
    records_dir = out_dir / "records"
    records_dir.mkdir(parents=True)
    # go run ./cmd/runpilot is faked to do nothing (see _fake_subprocess), so
    # these four records stand in for what it would have written for the
    # four (rho, scheduler) combinations.
    for rho, scheduler in [(0.5, "LeastAllocated"), (0.5, "MostAllocated"), (1.1, "LeastAllocated"), (1.1, "MostAllocated")]:
        _write_record(records_dir / f"rho{rho}-{scheduler}.jsonl", _good_combo_record(rho, scheduler))
    nodes_csv, pods_csv = tmp_path / "nodes.csv", tmp_path / "openb_pod_list_test.csv"
    nodes_csv.write_text("sn,cpu_milli,memory_mib,gpu,model\nnode-1,4000,8192,0,\n")
    pods_csv.write_text(POD_CSV_HEADER + "pod-1,100,100,0,0,,BestEffort,Running,0,10,0\n")

    exit_code = run_pilot.run(
        ["--nodes", str(nodes_csv), "--pods", str(pods_csv), "--seed", "1", "--ratio", "1.0", "--out", str(out_dir)],
        run_subprocess=_make_fake_subprocess(_GOOD_BUILDINPUT_STDOUT),
    )

    captured = capsys.readouterr()
    # _good_combo_record's single-pod fixtures can't exercise every Then/
    # sanity check (there is no real rho or scheduler effect for them to
    # show), so at least one check fails and run()'s exit code must reflect
    # that (units/U05.md: "1つでも落ちていれば、0でない終了コードを返す"),
    # while checks a minimal fixture *can* satisfy still pass and print as
    # such — both lines must actually appear, not just one or the other.
    assert "[PASS]" in captured.out
    assert "[FAIL]" in captured.out
    assert exit_code == 1


def test_run_returns_zero_and_prints_only_pass_lines_when_every_check_passes(tmp_path: Path, capsys: pytest.CaptureFixture[str]) -> None:
    # Reusing _fully_passing_run_all_checks_kwargs's records as-is does not
    # reach exit_code == 0 here: unlike run_all_checks (which takes
    # report_text/existing_figures directly), run() generates the report for
    # real from these records via aggregate.run(), which (1) needs each
    # record's scheduler_profile to equal run()'s own --distributed-scheduler
    # value exactly (the full schedulerprofile.Pilot description, not the
    # bare name _fully_passing_record uses) for scenario 6's prediction
    # section to resolve at all, and (2) needs an actual GPU-8 class and
    # resource-fragmentation scenario in the data for that section's wait:8/
    # unplaceable_free rows to have something to show. Found while
    # attempting the reuse the round-2 review suggested; this dedicated
    # fixture adds both instead of changing the shared one every other test
    # in this file already relies on.
    least_desc = run_pilot._LEAST_ALLOCATED_DESCRIPTION
    most_desc = (
        "NodeResourcesFit(scoringStrategy=MostAllocated, resources=cpu:1,memory:1,nvidia.com/gpu:1); NodeResourcesBalancedAllocation disabled"
    )

    def record(rho: float, scheduler_profile: str, running_per_node: dict[str, int], cpu_allocated: str) -> aggregate.RunRecord:
        node_per_pod = [node_id for node_id, count in running_per_node.items() for _ in range(count)]
        pods = [(f"task-{i}", f"instance-{i}", node_id) for i, node_id in enumerate(node_per_pod, start=1)]
        b = _Builder()
        for node_id in running_per_node:
            b.register_node(0, node_id, {"cpu": "100"})
        for task_id, instance_id, _ in pods:
            b.create_task(0, task_id, instance_id, cpu=cpu_allocated)
        # Stays pending the whole time: too large (150 cpu) for either
        # 100-cpu node alone, but fits the cluster's combined free cpu, so it
        # fragments (scenario 6's unplaceable_free row); its GPU-8 request
        # also gives scenario 6's wait:8 row a nonzero, right-censored wait.
        b.create_task(0, "task-large", "instance-large", cpu="150", gpu="8")
        for task_id, instance_id, node_id in pods:
            b.bind(5, task_id, instance_id, node_id, {"cpu": cpu_allocated})
        for task_id, instance_id, node_id in pods:
            b.complete(8, task_id, instance_id, node_id, {"cpu": cpu_allocated})
        b.create_task(10, "task-end", "instance-end", cpu="0")  # stays pending; marks the end of observation
        return aggregate.RunRecord(header=_header(rho=rho, scheduler_profile=scheduler_profile), transitions=b.transitions)

    records = [
        record(0.5, least_desc, {"node-1": 1, "node-2": 1}, cpu_allocated="1"),
        record(0.5, most_desc, {"node-1": 2, "node-2": 0}, cpu_allocated="1"),
        record(1.1, least_desc, {"node-1": 1, "node-2": 1}, cpu_allocated="4"),
        record(1.1, most_desc, {"node-1": 2, "node-2": 0}, cpu_allocated="4"),
    ]

    out_dir = tmp_path / "out"
    records_dir = out_dir / "records"
    records_dir.mkdir(parents=True)
    for i, r in enumerate(records):
        _write_record(records_dir / f"record-{i}.jsonl", r)
    nodes_csv, pods_csv = tmp_path / "nodes.csv", tmp_path / "openb_pod_list_test.csv"
    nodes_csv.write_text("sn,cpu_milli,memory_mib,gpu,model\nnode-1,4000,8192,0,\n")
    # Every record above creates 4 pods (2 running + 1 large-pending + 1
    # marker); eligible_pods=4 x ratio=1.0 behind the summary below needs the
    # same count of non-Pending rows here (run() derives eligible_pods from
    # this CSV itself, not from the records).
    pods_csv.write_text(
        POD_CSV_HEADER
        + "pod-1,100,100,0,0,,BestEffort,Running,0,10,0\n"
        + "pod-2,100,100,0,0,,BestEffort,Running,0,10,0\n"
        + "pod-3,100,100,0,0,,BestEffort,Running,0,10,0\n"
        + "pod-4,100,100,0,0,,BestEffort,Running,0,10,0\n"
    )

    exit_code = run_pilot.run(
        ["--nodes", str(nodes_csv), "--pods", str(pods_csv), "--seed", "1", "--ratio", "1.0", "--out", str(out_dir)],
        run_subprocess=_make_fake_subprocess({"0.5": _buildinput_stdout(4, 0.5), "1.1": _buildinput_stdout(4, 1.1)}),
    )

    captured = capsys.readouterr()
    assert "[PASS]" in captured.out
    assert "[FAIL]" not in captured.out, captured.out
    assert exit_code == 0


# --- run (integration) ---

_TINY_NODE_CSV = "sn,cpu_milli,memory_mib,gpu,model\nnode-1,4000,8192,0,\n"
_TINY_POD_CSV = (
    POD_CSV_HEADER
    + "pod-1,100,100,0,0,,BestEffort,Running,1000,1010,1000\n"
    + "pod-2,100,100,0,0,,BestEffort,Running,1050,1070,1050\n"
    + "pod-3,100,100,0,0,,BestEffort,Failed,1100,1110,1100\n"
    + "pod-4,100,100,0,0,,BestEffort,Running,1150,1160,1150\n"
    + "pod-5,100,100,0,0,,BestEffort,Pending,1200,0,\n"
)


@pytest.mark.integration
def test_run_builds_records_and_report_from_a_tiny_openb_style_csv(tmp_path: Path) -> None:
    # Exercises the whole wiring (go run ./cmd/buildinput x2, go run
    # ./cmd/runpilot, aggregate.run in-process) against a tiny hand-built
    # input, the same kind of fast dry run units/U05.md's "着手時に決める
    # こと" calls for before scaling up to the full openb trace.
    nodes_csv = tmp_path / "nodes.csv"
    # cmd/runpilot requires --pods' file name to start with "openb" (it reads
    # the trace version from it); a plainly-named "pods.csv" fails that check.
    pods_csv = tmp_path / "openb_pod_list_test.csv"
    nodes_csv.write_text(_TINY_NODE_CSV)
    pods_csv.write_text(_TINY_POD_CSV)
    out_dir = tmp_path / "out"

    exit_code = run_pilot.run(
        [
            "--nodes",
            str(nodes_csv),
            "--pods",
            str(pods_csv),
            "--seed",
            "1",
            "--ratio",
            "1.0",
            "--out",
            str(out_dir),
        ]
    )

    record_files = list((out_dir / "records").glob("*.jsonl"))
    assert len(record_files) == 4
    assert (out_dir / "report" / "report.md").exists()
    # This tiny, 4-pod input is too small to show a real rho or scheduler
    # effect, or to sample a large-GPU-request pod for scenario 6, so a
    # confirmed 3 of the 46 checks fail and exit_code is 1, not 0 — that is
    # the expected result for *this* input, not an unrelated regression.
    assert exit_code == 1


if __name__ == "__main__":
    # "-m not integration" is always prepended, not only when no extra args
    # are given: pytest keeps the *last* -m it is passed, so any extra args
    # (e.g. run_checks.sh passing none at all, or a human adding "-q") still
    # exclude the slow integration test by default, and only an explicit
    # "-m ..." of the caller's own overrides it (e.g. "-m integration" to run
    # only that one). A plain `len(sys.argv) > 1` check does not do this: it
    # treated *any* extra argument, including "-q", as opting out of the
    # default and ran the integration test anyway.
    raise SystemExit(pytest.main([__file__, "-m", "not integration", *sys.argv[1:]]))

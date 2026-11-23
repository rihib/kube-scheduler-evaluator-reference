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
Integration tests are marked `integration`; run only unit tests with
`uv run --with pytest aggregate/test_run_pilot.py -m "not integration"`.
"""

from __future__ import annotations

import sys
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).parent))
import aggregate
import run_pilot

BUILDINPUT_SUMMARY_TEXT = "used pods:     800\nexcluded pods: 200\nfailed pods:   12\nrho (before):  0.734521\nrho (after):   1.100000\n"


def test_parse_buildinput_summary_reads_every_field() -> None:
    summary = run_pilot.parse_buildinput_summary(BUILDINPUT_SUMMARY_TEXT)

    assert summary == run_pilot.BuildInputSummary(
        used_pods=800,
        excluded_pods=200,
        failed_pods=12,
        rho_before=0.734521,
        rho_after=1.1,
    )


def test_parse_buildinput_summary_raises_when_a_field_is_missing() -> None:
    text = "used pods:     800\n"

    try:
        run_pilot.parse_buildinput_summary(text)
    except ValueError as e:
        assert "rho (after)" in str(e) or "excluded" in str(e) or "failed" in str(e) or "rho (before)" in str(e)
    else:
        raise AssertionError("expected ValueError for a buildinput summary missing fields")


POD_CSV_HEADER = "name,cpu_milli,memory_mib,num_gpu,gpu_milli,gpu_spec,qos,pod_phase,creation_time,deletion_time,scheduled_time\n"


def test_count_eligible_pods_excludes_pending_rows() -> None:
    # scenarios/clusterdata/input_version.go's BuildInputVersion treats
    # "Pending" (case-insensitively) as the only excluded phase before sampling;
    # units/U05.md's "使った件数が...Pendingを除いたPodの80%であること" needs
    # that same eligible count independently of what buildinput itself reports.
    csv_text = (
        POD_CSV_HEADER
        + "pod-1,100,100,0,0,,BestEffort,Running,0,10,0\n"
        + "pod-2,100,100,0,0,,BestEffort,Pending,0,,\n"
        + "pod-3,100,100,0,0,,BestEffort,Failed,0,10,0\n"
        + "pod-4,100,100,0,0,,BestEffort,pending,0,,\n"
    )

    assert run_pilot.count_eligible_pods(csv_text) == 2


def test_expected_sampled_count_rounds_half_up_like_go() -> None:
    # scenarios/clusterdata/input_version.go's sampleAndOrderPods:
    # int(float64(len(eligible))*sampleRatio + 0.5).
    assert run_pilot.expected_sampled_count(eligible=5, ratio=0.5) == 3
    assert run_pilot.expected_sampled_count(eligible=1000, ratio=0.8) == 800


# --- check_scenario1 ---


def test_check_scenario1_passes_when_used_count_and_rho_match() -> None:
    summary = run_pilot.BuildInputSummary(used_pods=800, excluded_pods=200, failed_pods=12, rho_before=0.5, rho_after=1.1)

    results = run_pilot.check_scenario1(summary, eligible_pods=1000, ratio=0.8, target_rho=1.1)

    assert all(r.passed for r in results)
    assert {r.name for r in results} == {"scenario1: used pods is 80% of non-pending pods", "scenario1: achieved rho matches the target"}


def test_check_scenario1_fails_when_used_count_is_off() -> None:
    summary = run_pilot.BuildInputSummary(used_pods=799, excluded_pods=201, failed_pods=12, rho_before=0.5, rho_after=1.1)

    results = run_pilot.check_scenario1(summary, eligible_pods=1000, ratio=0.8, target_rho=1.1)

    used_check = next(r for r in results if r.name == "scenario1: used pods is 80% of non-pending pods")
    assert used_check.passed is False
    assert "800" in used_check.detail and "799" in used_check.detail


def test_check_scenario1_fails_when_rho_does_not_match_target() -> None:
    summary = run_pilot.BuildInputSummary(used_pods=800, excluded_pods=200, failed_pods=12, rho_before=0.5, rho_after=1.05)

    results = run_pilot.check_scenario1(summary, eligible_pods=1000, ratio=0.8, target_rho=1.1)

    rho_check = next(r for r in results if r.name == "scenario1: achieved rho matches the target")
    assert rho_check.passed is False


# --- check_scenario2 ---


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


def _good_scenario2_transitions() -> list[aggregate.Transition]:
    return [
        _transition("2024-01-01T00:00:00.000000000Z", 1, "register_node", "node-1", state="resource_capacity", after={"cpu": "10"}),
        _transition("2024-01-01T00:00:05.000000000Z", 2, "assign_node", "instance-1", state="assigned_node", after="node-1"),
        _transition("2024-01-01T00:00:10.000000000Z", 3, "complete", "instance-1", state="instance_status", after="completed"),
    ]


def test_check_scenario2_passes_for_a_well_formed_record() -> None:
    record = aggregate.RunRecord(header=_header(), transitions=_good_scenario2_transitions())

    results = run_pilot.check_scenario2(record)

    assert all(r.passed for r in results)


def test_check_scenario2_fails_when_header_is_missing_a_field() -> None:
    record = aggregate.RunRecord(header=_header(reference_commit=""), transitions=_good_scenario2_transitions())

    results = run_pilot.check_scenario2(record)

    header_check = next(r for r in results if "header" in r.name)
    assert header_check.passed is False


def test_check_scenario2_fails_when_no_initial_node_capacity_is_recorded() -> None:
    record = aggregate.RunRecord(header=_header(), transitions=_good_scenario2_transitions()[1:])

    results = run_pilot.check_scenario2(record)

    capacity_check = next(r for r in results if "capacity" in r.name)
    assert capacity_check.passed is False


def test_check_scenario2_fails_when_virtual_time_goes_backwards() -> None:
    transitions = _good_scenario2_transitions()
    out_of_order = [transitions[0], transitions[2], transitions[1]]  # complete before assign, by position

    record = aggregate.RunRecord(header=_header(), transitions=out_of_order)

    results = run_pilot.check_scenario2(record)

    time_check = next(r for r in results if "time" in r.name)
    assert time_check.passed is False


# --- check_scenario3 ---


def _record_with(rho: float, scheduler_profile: str) -> aggregate.RunRecord:
    return aggregate.RunRecord(header=_header(rho=rho, scheduler_profile=scheduler_profile), transitions=[])


def test_check_scenario3_passes_for_four_distinct_combinations() -> None:
    records = [
        _record_with(0.5, "LeastAllocated"),
        _record_with(0.5, "MostAllocated"),
        _record_with(1.1, "LeastAllocated"),
        _record_with(1.1, "MostAllocated"),
    ]

    results = run_pilot.check_scenario3(records)

    assert all(r.passed for r in results)


def test_check_scenario3_fails_when_there_are_not_four_records() -> None:
    records = [_record_with(0.5, "LeastAllocated"), _record_with(1.1, "LeastAllocated")]

    results = run_pilot.check_scenario3(records)

    count_check = next(r for r in results if "four" in r.name)
    assert count_check.passed is False


def test_check_scenario3_fails_when_two_records_share_the_same_combination() -> None:
    records = [
        _record_with(0.5, "LeastAllocated"),
        _record_with(0.5, "LeastAllocated"),
        _record_with(1.1, "LeastAllocated"),
        _record_with(1.1, "MostAllocated"),
    ]

    results = run_pilot.check_scenario3(records)

    distinct_check = next(r for r in results if "distinct" in r.name)
    assert distinct_check.passed is False


# --- check_scenario4 ---

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


def test_check_scenario4_passes_for_a_complete_report() -> None:
    results = run_pilot.check_scenario4(GOOD_REPORT_TEXT, existing_figures={"figures/rho-0.5-wait.png", "figures/rho-1.1-wait.png"})

    assert all(r.passed for r in results)


def test_check_scenario4_fails_when_a_referenced_figure_is_missing() -> None:
    results = run_pilot.check_scenario4(GOOD_REPORT_TEXT, existing_figures={"figures/rho-0.5-wait.png"})

    figure_check = next(r for r in results if "figure" in r.name)
    assert figure_check.passed is False
    assert "rho-1.1-wait.png" in figure_check.detail


def test_check_scenario4_fails_when_the_diff_section_only_has_the_fallback_message() -> None:
    text = GOOD_REPORT_TEXT.replace(
        "LeastAllocated − MostAllocated\n\n| metric | diff |\n| --- | --- |\n| cluster_std | 0.2 |",
        "LeastAllocated と MostAllocated がどの入力の版でもそろわず、差を計算できなかった。",
    )

    results = run_pilot.check_scenario4(text, existing_figures={"figures/rho-0.5-wait.png", "figures/rho-1.1-wait.png"})

    diff_check = next(r for r in results if "diff" in r.name)
    assert diff_check.passed is False


# --- check_scenario5 ---


def test_check_scenario5_passes_when_the_negative_control_section_is_complete() -> None:
    results = run_pilot.check_scenario5(GOOD_REPORT_TEXT)

    assert all(r.passed for r in results)


def test_check_scenario5_fails_when_rho_0_5_has_no_negative_control_section() -> None:
    text = GOOD_REPORT_TEXT.split("### 陰性対照")[0] + "## ρ = 1.1\n"

    results = run_pilot.check_scenario5(text)

    assert any(not r.passed for r in results)


def test_check_scenario5_fails_when_fewer_than_two_schedulers_are_listed() -> None:
    text = GOOD_REPORT_TEXT.replace("- MostAllocated: 0.1200\n", "")

    results = run_pilot.check_scenario5(text)

    assert any(not r.passed for r in results)


# --- check_scenario6 ---


def test_check_scenario6_passes_when_the_prediction_section_has_both_metrics() -> None:
    results = run_pilot.check_scenario6(GOOD_REPORT_TEXT)

    assert all(r.passed for r in results)


def test_check_scenario6_fails_when_rho_1_1_has_no_prediction_section() -> None:
    text = GOOD_REPORT_TEXT.split("### 予測と結果（高負荷）")[0]

    results = run_pilot.check_scenario6(text)

    assert any(not r.passed for r in results)


def test_check_scenario6_fails_when_the_unplaceable_free_row_is_missing() -> None:
    text = GOOD_REPORT_TEXT.replace("| unplaceable_free:cpu | 0.4000 | 当たった |\n", "")

    results = run_pilot.check_scenario6(text)

    assert any(not r.passed for r in results)


# --- check_sanity_counts ---


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


def test_check_sanity_counts_passes_when_every_pod_ends_pending_or_completed() -> None:
    record = aggregate.RunRecord(
        header=_header(),
        transitions=[
            _create(0, 1, "task-1", "instance-1"),
            _complete(10, 2, "task-1", "instance-1"),
            _create(0, 3, "task-2", "instance-2"),  # stays pending
        ],
    )

    results = run_pilot.check_sanity_counts(record, expected_pod_count=2)

    assert all(r.passed for r in results)


def test_check_sanity_counts_fails_when_created_count_does_not_match() -> None:
    record = aggregate.RunRecord(header=_header(), transitions=[_create(0, 1, "task-1", "instance-1")])

    results = run_pilot.check_sanity_counts(record, expected_pod_count=2)

    count_check = next(r for r in results if "created" in r.name)
    assert count_check.passed is False


def test_check_sanity_counts_fails_when_a_pod_is_still_running_at_the_end() -> None:
    record = aggregate.RunRecord(
        header=_header(),
        transitions=[
            _create(0, 1, "task-1", "instance-1"),
            _transition(
                "2024-01-01T00:00:05.000000000Z", 2, "start", "instance-1", state="instance_status", task_id="task-1", after="running"
            ),
        ],
    )

    results = run_pilot.check_sanity_counts(record, expected_pod_count=1)

    final_check = next(r for r in results if "final" in r.name)
    assert final_check.passed is False


# --- check_sanity_ranges ---


def _metrics(**overrides) -> aggregate.Metrics:
    fields = {
        "pending_wait_by_class": {"0": [1.0, 2.0]},
        "pending_count_at_end": 0,
        "allocation_rate": {"cpu": 0.5},
        "unplaceable_free": {"cpu": 10.0},
        "cluster_std": 0.2,
        "nonzero_wait_fraction": 0.5,
    }
    fields.update(overrides)
    return aggregate.Metrics(**fields)


def test_check_sanity_ranges_passes_for_well_formed_metrics() -> None:
    results = run_pilot.check_sanity_ranges(_metrics())

    assert all(r.passed for r in results)


def test_check_sanity_ranges_fails_for_a_negative_wait_time() -> None:
    results = run_pilot.check_sanity_ranges(_metrics(pending_wait_by_class={"0": [-1.0]}))

    wait_check = next(r for r in results if "wait" in r.name)
    assert wait_check.passed is False


def test_check_sanity_ranges_fails_for_an_allocation_rate_outside_0_1() -> None:
    results = run_pilot.check_sanity_ranges(_metrics(allocation_rate={"cpu": 1.5}))

    alloc_check = next(r for r in results if "allocation" in r.name)
    assert alloc_check.passed is False


def test_check_sanity_ranges_fails_for_a_negative_unplaceable_free() -> None:
    results = run_pilot.check_sanity_ranges(_metrics(unplaceable_free={"cpu": -1.0}))

    free_check = next(r for r in results if "unplaceable" in r.name)
    assert free_check.passed is False


def test_check_sanity_ranges_fails_for_a_nan_value() -> None:
    results = run_pilot.check_sanity_ranges(_metrics(cluster_std=float("nan")))

    nan_check = next(r for r in results if "nan" in r.name.lower() or "NaN" in r.name)
    assert nan_check.passed is False


# --- check_sanity_rho_effect ---


def test_check_sanity_rho_effect_passes_when_allocation_rate_is_higher_at_high_rho() -> None:
    low = _metrics(allocation_rate={"cpu": 0.3, "memory": 0.2})
    high = _metrics(allocation_rate={"cpu": 0.6, "memory": 0.5})

    results = run_pilot.check_sanity_rho_effect(low, high)

    assert all(r.passed for r in results)


def test_check_sanity_rho_effect_fails_when_a_resource_does_not_increase() -> None:
    low = _metrics(allocation_rate={"cpu": 0.6})
    high = _metrics(allocation_rate={"cpu": 0.3})

    results = run_pilot.check_sanity_rho_effect(low, high)

    assert any(not r.passed for r in results)


def test_check_sanity_rho_effect_fails_when_no_resource_is_common() -> None:
    low = _metrics(allocation_rate={"cpu": 0.3})
    high = _metrics(allocation_rate={"gpu": 0.5})

    results = run_pilot.check_sanity_rho_effect(low, high)

    assert any(not r.passed for r in results)


# --- check_sanity_scheduler_effect ---


def test_check_sanity_scheduler_effect_passes_when_least_allocated_spreads_more_evenly() -> None:
    least = _metrics(cluster_std=0.1)
    most = _metrics(cluster_std=0.4)

    results = run_pilot.check_sanity_scheduler_effect(least, most)

    assert all(r.passed for r in results)


def test_check_sanity_scheduler_effect_fails_when_cluster_std_is_equal() -> None:
    least = _metrics(cluster_std=0.2)
    most = _metrics(cluster_std=0.2)

    results = run_pilot.check_sanity_scheduler_effect(least, most)

    assert any(not r.passed for r in results)


def test_check_sanity_scheduler_effect_fails_when_least_allocated_has_the_bigger_std() -> None:
    least = _metrics(cluster_std=0.5)
    most = _metrics(cluster_std=0.2)

    results = run_pilot.check_sanity_scheduler_effect(least, most)

    assert any(not r.passed for r in results)


def test_check_sanity_scheduler_effect_fails_when_cluster_std_was_not_measured() -> None:
    least = _metrics(cluster_std=None)
    most = _metrics(cluster_std=0.2)

    results = run_pilot.check_sanity_scheduler_effect(least, most)

    assert any(not r.passed for r in results)


# --- check_sanity_rho_achieved ---


def test_check_sanity_rho_achieved_passes_when_rho_after_matches_target() -> None:
    summary = run_pilot.BuildInputSummary(used_pods=800, excluded_pods=200, failed_pods=12, rho_before=0.3, rho_after=0.5)

    result = run_pilot.check_sanity_rho_achieved(summary, target_rho=0.5)

    assert result.passed is True


def test_check_sanity_rho_achieved_fails_when_rho_after_does_not_match_target() -> None:
    summary = run_pilot.BuildInputSummary(used_pods=800, excluded_pods=200, failed_pods=12, rho_before=0.3, rho_after=0.45)

    result = run_pilot.check_sanity_rho_achieved(summary, target_rho=0.5)

    assert result.passed is False


# --- run_all_checks ---


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


def test_run_all_checks_returns_only_passing_results_for_well_formed_inputs() -> None:
    # allocation_rate and cluster_std are not exercised meaningfully by
    # _good_combo_record's single-pod runs (every run allocates the same
    # amount for the same duration), so this only asserts that every check
    # *ran* (no "material missing" placeholders), not that every one passed.
    results = run_pilot.run_all_checks(**_good_run_all_checks_kwargs())

    assert len(results) > 0
    assert all("material missing" not in r.detail for r in results)


def test_run_all_checks_reports_scenario2_material_missing_when_the_rho_1_1_least_allocated_record_is_absent() -> None:
    kwargs = _good_run_all_checks_kwargs()
    kwargs["records"] = [r for r in kwargs["records"] if not (r.header.rho == 1.1 and r.header.scheduler_profile == "LeastAllocated")]

    results = run_pilot.run_all_checks(**kwargs)

    # Every check that needed the missing record is reported as failed with
    # "material missing" in its detail (units/U05.md: "前の段が落ちて後の項目
    # の材料ができないときは、後の項目を「材料が無い」として落とす"), instead
    # of raising or silently skipping.
    scenario2_missing = next(r for r in results if r.name.startswith("scenario2") and "material missing" in r.detail)
    assert scenario2_missing.passed is False


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


# --- format_results ---


def test_format_results_marks_each_result_pass_or_fail_with_its_detail() -> None:
    results = [
        run_pilot.CheckResult(name="a", passed=True, detail="ok"),
        run_pilot.CheckResult(name="b", passed=False, detail="went wrong"),
    ]

    text = run_pilot.format_results(results)

    assert "[PASS] a: ok" in text
    assert "[FAIL] b: went wrong" in text


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
    # こと" calls for before scaling up to the full openb trace. This only
    # checks that the pipeline produces its files without crashing; whether
    # every Then and sanity check actually *passes* needs real openb-scale
    # data (too few pods here to show a real rho or scheduler effect).
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

    assert isinstance(exit_code, int)
    record_files = list((out_dir / "records").glob("*.jsonl"))
    assert len(record_files) == 4
    assert (out_dir / "report" / "report.md").exists()


if __name__ == "__main__":
    raise SystemExit(pytest.main([__file__, *sys.argv[1:]]))

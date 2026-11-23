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

BUILDINPUT_SUMMARY_TEXT = (
    "used pods:     800\n"
    "excluded pods: 200\n"
    "failed pods:   12\n"
    "rho (before):  0.734521\n"
    "rho (after):   1.100000\n"
)


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
    fields = dict(
        evaluation_id="eval-1",
        scenario_id="rho1p1-leastallocated",
        iteration=0,
        trace_name="openb",
        trace_version="openb_pod_list_default",
        reference_commit="abc123",
        input_seed="1",
        rho=1.1,
        evaluator_commit="def456",
        hardware="test-machine",
        scheduler_profile="LeastAllocated",
    )
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


if __name__ == "__main__":
    raise SystemExit(pytest.main([__file__]))

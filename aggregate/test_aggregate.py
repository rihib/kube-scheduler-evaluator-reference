# /// script
# requires-python = ">=3.12"
# dependencies = [
#   "numpy",
#   "pandas",
#   "matplotlib",
# ]
# ///

"""Unit and integration tests for aggregate.py.

Run with: uv run --with pytest aggregate/test_aggregate.py
Integration tests are marked `integration`; run only unit tests with
`uv run --with pytest aggregate/test_aggregate.py -m "not integration"`.
"""

from __future__ import annotations

import json
import sys
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).parent))
import aggregate


def record_text(header: dict, transitions: list[dict]) -> str:
    """Builds one record's raw JSONL text (header line + transition lines), the
    shape parse_record takes."""
    lines = [json.dumps(header)] + [json.dumps(t) for t in transitions]
    return "\n".join(lines) + "\n"


def write_record_file(path: Path, header: dict, transitions: list[dict]) -> None:
    path.write_text(record_text(header, transitions))


def sample_header(**overrides) -> dict:
    header = {
        "evaluation_id": "eval-1",
        "scenario_id": "scenario-1",
        "iteration": 0,
        "input": {
            "trace_name": "openb",
            "trace_version": "v1",
            "reference_commit": "abc123",
            "input_seed": "seed-1",
            "rho": 0.5,
        },
        "execution": {
            "evaluator_commit": "def456",
            "reference_commit": "abc123",
            "hardware": "test-machine",
        },
        "scheduler": {"profile": "LeastAllocated"},
    }
    header.update(overrides)
    return header


SAMPLE_TRANSITIONS = [
    {
        "time": "2024-01-01T00:00:00.000000000Z",
        "seq": 1,
        "operation": "create_task",
        "component_id": "instance-1",
        "task_id": "task-1",
        "state": "instance_status",
        "before": None,
        "after": "pending",
        "task_immutable": {
            "owner": "u1",
            "group": "g1",
            "job_id": "job-1",
            "resource_request": {"cpu": "1", "memory": "1Gi", "nvidia.com/gpu": "0"},
            "resource_limit": {"cpu": "1", "memory": "1Gi", "nvidia.com/gpu": "0"},
            "priority": 0,
        },
    },
    {
        "time": "2024-01-01T00:00:10.000000000Z",
        "seq": 2,
        "operation": "assign_node",
        "component_id": "instance-1",
        "task_id": "task-1",
        "state": "assigned_node",
        "before": None,
        "after": "node-1",
    },
]


# --- parse_record ---


def test_parse_record_returns_header_fields() -> None:
    text = record_text(sample_header(), [])

    record = aggregate.parse_record(text)

    assert record.header.trace_name == "openb"
    assert record.header.trace_version == "v1"
    assert record.header.input_seed == "seed-1"
    assert record.header.rho == 0.5
    assert record.header.scheduler_profile == "LeastAllocated"
    assert record.header.iteration == 0
    assert record.truncated is False


def test_parse_record_returns_transitions_in_order() -> None:
    text = record_text(sample_header(), SAMPLE_TRANSITIONS)

    record = aggregate.parse_record(text)

    assert len(record.transitions) == 2
    assert record.transitions[0].operation == "create_task"
    assert record.transitions[0].seq == 1
    assert record.transitions[0].task_immutable["owner"] == "u1"
    assert record.transitions[1].operation == "assign_node"
    assert record.transitions[1].after == "node-1"


def test_parse_record_raises_when_the_file_is_empty() -> None:
    with pytest.raises(ValueError, match="empty"):
        aggregate.parse_record("")


def test_parse_record_marks_truncated_when_the_last_line_is_cut_mid_write() -> None:
    # A write interrupted mid-line (units/U04.md "着手時に決めること") leaves a
    # dangling fragment, not valid JSON, on the last line.
    text = record_text(sample_header(), SAMPLE_TRANSITIONS) + '{"time": "2024-01-01T00:00:20'

    record = aggregate.parse_record(text)

    assert record.truncated is True
    # The dropped fragment carried no complete transition, so the two complete
    # ones before it are still readable.
    assert len(record.transitions) == 2


def test_parse_record_is_not_truncated_for_a_genuinely_short_complete_record() -> None:
    # A record that is short because the run itself was short must not be
    # mistaken for a truncated one: nothing here is cut off mid-line.
    text = record_text(sample_header(), SAMPLE_TRANSITIONS[:1])

    record = aggregate.parse_record(text)

    assert record.truncated is False
    assert len(record.transitions) == 1


def test_parse_record_raises_when_a_non_last_line_is_malformed() -> None:
    # Only the last line being malformed is explained by a mid-write cut; a
    # malformed line anywhere else is a different, genuine corruption.
    text = record_text(sample_header(), [])[:-1] + "\nnot json at all\n" + json.dumps(SAMPLE_TRANSITIONS[0]) + "\n"

    with pytest.raises(json.JSONDecodeError):
        aggregate.parse_record(text)


def test_parse_record_raises_when_only_line_is_cut_mid_write() -> None:
    # Dropping the sole (truncated) line leaves nothing to parse a header from.
    with pytest.raises(ValueError, match="empty"):
        aggregate.parse_record('{"evaluation_id": "eval-1"')


# --- read_record_texts_from_folder ---


def test_parse_record_raises_when_the_header_line_is_not_a_json_object() -> None:
    # round3 review, correctness IMO: a line that is valid JSON but not an
    # object is a malformed record, which run() reports with the file's name.
    # Without this check it crashed deeper in with a TypeError that run()'s
    # narrowed except does not catch, so the reader never learned which file
    # was bad.
    with pytest.raises(ValueError, match="JSON object"):
        aggregate.parse_record("123\n")


def test_parse_record_raises_when_a_transition_line_is_not_a_json_object() -> None:
    header = json.dumps(sample_header())
    with pytest.raises(ValueError, match="JSON object"):
        aggregate.parse_record(f"{header}\n[1, 2]\n")


@pytest.mark.parametrize("field", ["input", "execution", "scheduler"])
def test_parse_record_raises_when_a_header_section_is_not_a_json_object(field: str) -> None:
    # round4 review, correctness IMO: the header line being an object is not
    # enough. Its sections are read one level deeper, so a record where one of
    # them is a bare value crashed with a TypeError that run()'s except does
    # not catch, losing the name of the file that was corrupt.
    text = record_text(sample_header(**{field: "openb"}), [])

    with pytest.raises(ValueError, match=f'header field "{field}" is not a JSON object'):
        aggregate.parse_record(text)


def test_read_record_texts_from_folder_reads_every_jsonl_file(tmp_path: Path) -> None:
    write_record_file(tmp_path / "run-a.jsonl", sample_header(scheduler={"profile": "LeastAllocated"}), [])
    write_record_file(tmp_path / "run-b.jsonl", sample_header(scheduler={"profile": "MostAllocated"}), [])
    (tmp_path / "not-a-record.txt").write_text("ignore me")

    texts = aggregate.read_record_texts_from_folder(tmp_path)

    assert len(texts) == 2
    paths = [path for path, _ in texts]
    assert all(p.suffix == ".jsonl" for p in paths)
    assert sum("LeastAllocated" in t for _, t in texts) == 1
    assert sum("MostAllocated" in t for _, t in texts) == 1


# --- parse_quantity ---


@pytest.mark.parametrize(
    "value,expected",
    [
        ("0", 0.0),
        ("4", 4.0),
        ("500m", 0.5),
        ("1500m", 1.5),
        ("1Gi", 1024.0**3),
        ("2Ki", 2.0 * 1024),
        ("1G", 1_000_000_000.0),
    ],
)
def test_parse_quantity(value: str, expected: float) -> None:
    assert aggregate.parse_quantity(value) == pytest.approx(expected)


# --- classify_gpu_class ---


@pytest.mark.parametrize(
    "gpu_count,expected_class",
    [(0, "0"), (1, "1"), (2, "2-4"), (4, "2-4"), (8, "8")],
)
def test_classify_gpu_class(gpu_count: int, expected_class: str) -> None:
    assert aggregate.classify_gpu_class(gpu_count) == expected_class


# --- parse_time ---


def test_parse_time_orders_chronologically() -> None:
    a = aggregate.parse_time("2024-01-01T00:00:00.000000000Z")
    b = aggregate.parse_time("2024-01-01T00:00:10.500000000Z")
    assert b - a == pytest.approx(10.5)


def test_parse_time_same_instant_is_equal() -> None:
    a = aggregate.parse_time("2024-01-01T00:00:00.000000000Z")
    b = aggregate.parse_time("2024-01-01T00:00:00.000000000Z")
    assert a == b


def test_parse_time_handles_a_non_utc_offset() -> None:
    # Round1 review, test IMO: the +HH:MM/-HH:MM branch (record.go's time format
    # can carry one, even though the evaluator always writes "Z" today) had no
    # test.
    utc = aggregate.parse_time("2024-01-01T00:00:00.000000000Z")
    plus_nine = aggregate.parse_time("2024-01-01T09:00:00.000000000+09:00")
    assert utc == pytest.approx(plus_nine)


def test_parse_time_rejects_an_invalid_string() -> None:
    # Round1 review, test IMO: the "does not match the virtual-time format" error
    # path had no test.
    with pytest.raises(ValueError, match="not a virtual time"):
        aggregate.parse_time("not a time")


# --- compute_metrics ---


class RecordBuilder:
    """Builds a transition list with the same shape record.go's Manager writes,
    so metrics tests exercise compute_metrics the way a real record would."""

    def __init__(self) -> None:
        self._seq = 0
        self.transitions: list[aggregate.Transition] = []

    def _next_seq(self) -> int:
        self._seq += 1
        return self._seq

    def _write(self, time, op, component_id, task_id="", state="", state_key="", before=None, after=None, task_immutable=None):
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

    def register_node(self, time, node_id, capacity: dict[str, str]) -> RecordBuilder:
        self._write(time, "register_node", node_id, state="resource_capacity", after=capacity)
        return self

    def create_task(self, time, task_id, instance_id, cpu="1", memory="0", gpu="0") -> RecordBuilder:
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

    def bind(self, time, task_id, instance_id, node_id, allocated: dict[str, str]) -> RecordBuilder:
        self._write(time, "assign_node", instance_id, task_id=task_id, state="assigned_node", after=node_id)
        self._write(time, "assign_node", node_id, task_id=task_id, state="allocated_resource", state_key=instance_id, after=allocated)
        self._write(time, "start", instance_id, task_id=task_id, state="instance_status", before="pending", after="running")
        return self

    def complete(self, time, task_id, instance_id, node_id, allocated: dict[str, str]) -> RecordBuilder:
        # record.go's Complete: writes the completion, then releases the
        # instance's allocation (its own `release` helper).
        self._write(time, "complete", instance_id, task_id=task_id, state="instance_status", before="running", after="completed")
        self._write(time, "release", node_id, task_id=task_id, state="allocated_resource", state_key=instance_id, before=allocated, after=None)
        return self

    def deregister_node(self, time, node_id) -> RecordBuilder:
        self._write(time, "deregister_node", node_id, state="node_information", before=True, after=None)
        return self

    def delete(self, time, task_id, instance_id, node_id, allocated: dict[str, str]) -> RecordBuilder:
        # record.go's Delete: completion-independent removal (e.g. preemption).
        # Reverts assigned_node and releases the allocation, same as Complete.
        self._write(time, "delete", instance_id, task_id=task_id, state="instance_status", before="running", after="deleted")
        self._write(time, "delete", instance_id, task_id=task_id, state="assigned_node", before=node_id, after=None)
        self._write(time, "release", node_id, task_id=task_id, state="allocated_resource", state_key=instance_id, before=allocated, after=None)
        return self


def fragmented_record() -> RecordBuilder:
    """Two 5-cpu nodes and a pending 8-cpu task: the cluster has 10 free cpu in
    total, but no single node has 8, so _is_fragmentation_blocked reports
    fragmentation for the whole 10s observation (10 free cpu x 10s = 100)."""
    b = RecordBuilder()
    b.register_node(0, "node-1", {"cpu": "5"})
    b.register_node(0, "node-2", {"cpu": "5"})
    b.create_task(0, "task-1", "instance-1", cpu="8")
    b.create_task(10, "task-2", "instance-2", cpu="0")  # marks the end of observation
    return b


def unfragmented_record() -> RecordBuilder:
    """The same two 5-cpu nodes, but the pending task needs only 4 cpu, which
    node-1 alone can take: cpu is watched for the same 10s and never
    fragmented, so unplaceable_free:cpu is a measured 0."""
    b = RecordBuilder()
    b.register_node(0, "node-1", {"cpu": "5"})
    b.register_node(0, "node-2", {"cpu": "5"})
    b.create_task(0, "task-1", "instance-1", cpu="4")
    b.create_task(10, "task-2", "instance-2", cpu="0")  # marks the end of observation
    return b


def test_compute_metrics_pending_wait_by_gpu_class_and_count_at_end() -> None:
    b = RecordBuilder()
    b.create_task(0, "task-1", "instance-1", gpu="0")
    b.create_task(0, "task-2", "instance-2", gpu="1")
    b.create_task(2, "task-3", "instance-3", gpu="3")
    b.bind(5, "task-1", "instance-1", "node-1", {"cpu": "1"})
    b.bind(12, "task-3", "instance-3", "node-1", {"cpu": "1"})
    b.bind(15, "task-2", "instance-2", "node-1", {"cpu": "1"})
    b.create_task(20, "task-4", "instance-4", gpu="0")  # stays pending until the end
    b.create_task(20, "task-5", "instance-5", gpu="0")
    b.bind(20, "task-5", "instance-5", "node-1", {"cpu": "1"})  # wait == 0
    b.create_task(30, "task-6", "instance-6", gpu="0")  # created exactly at the end; marks the end too

    metrics = aggregate.compute_metrics(b.transitions)

    # instance-4 and instance-6 are still pending when the record ends (t=30), so their
    # dwell time so far (observation_end - pending_since) is a right-censored wait, not
    # excluded (round1 review, Must): excluding it would make a scheduler that leaves
    # more tasks stuck pending look like it waited *less*, the opposite of reality.
    assert metrics.pending_wait_by_class == {
        "0": [5.0, 0.0, 10.0, 0.0],
        "1": [15.0],
        "2-4": [10.0],
    }
    assert metrics.pending_count_at_end == 2
    assert metrics.nonzero_wait_fraction == pytest.approx(4 / 6)


def test_compute_metrics_allocation_rate_and_unplaceable_free() -> None:
    b = RecordBuilder()
    b.register_node(0, "node-1", {"cpu": "10"})
    b.create_task(0, "task-a", "instance-a", cpu="4")
    b.bind(10, "task-a", "instance-a", "node-1", {"cpu": "4"})
    b.create_task(20, "task-b", "instance-b", cpu="5")
    b.bind(25, "task-b", "instance-b", "node-1", {"cpu": "5"})
    b.create_task(30, "task-c", "instance-c", cpu="2")
    b.register_node(40, "node-2", {"cpu": "1"})
    b.create_task(50, "task-d", "instance-d", cpu="0")  # marks the end of observation

    metrics = aggregate.compute_metrics(b.transitions)

    assert metrics.allocation_rate["cpu"] == pytest.approx(0.553636364, rel=1e-6)
    assert metrics.unplaceable_free["cpu"] == pytest.approx(20.0)


def test_compute_metrics_cluster_std_is_time_weighted() -> None:
    b = RecordBuilder()
    b.register_node(0, "node-1", {"cpu": "10"})
    b.register_node(0, "node-2", {"cpu": "10"})
    b.create_task(0, "task-1", "instance-1", cpu="1")
    b.bind(10, "task-1", "instance-1", "node-1", {"cpu": "1"})
    b.create_task(10, "task-2", "instance-2", cpu="1")
    b.bind(20, "task-2", "instance-2", "node-1", {"cpu": "1"})
    b.create_task(20, "task-3", "instance-3", cpu="1")
    b.bind(30, "task-3", "instance-3", "node-2", {"cpu": "1"})
    b.create_task(40, "task-4", "instance-4", cpu="0")  # marks the end of observation

    metrics = aggregate.compute_metrics(b.transitions)

    assert metrics.cluster_std == pytest.approx(0.5)


def test_compute_metrics_reflects_complete_release_and_deregister_node() -> None:
    # Round1 review (test Must): complete, release, and deregister_node had no
    # test exercising them. A broken release (resources never freed, or freed
    # from the wrong node) or a deregistration that doesn't shrink capacity
    # would pass silently with allocation_rate/cluster_std just quietly wrong.
    b = RecordBuilder()
    b.register_node(0, "node-1", {"cpu": "10"})
    b.register_node(0, "node-2", {"cpu": "10"})
    b.create_task(0, "task-a", "instance-a", cpu="4")
    b.bind(0, "task-a", "instance-a", "node-1", {"cpu": "4"})
    b.complete(10, "task-a", "instance-a", "node-1", {"cpu": "4"})
    b.deregister_node(20, "node-1")
    b.create_task(25, "task-b", "instance-b", cpu="0")  # marks the end of observation

    metrics = aggregate.compute_metrics(b.transitions)

    # Two nodes, cap=20 total. [0,10): allocated=4/cap=20=0.2. [10,20): released,
    # allocated=0/cap=20=0.0. [20,25): node-1 deregistered, cap=10 (node-2 only),
    # allocated=0/cap=10=0.0. (0.2*10 + 0.0*10 + 0.0*5) / 25 = 0.08 — if release
    # left the 4 cpu allocated, this would be higher.
    assert metrics.allocation_rate["cpu"] == pytest.approx(0.08)
    # [0,10): node-1 has 1 running pod, node-2 has 0 -> std 0.5. [10,20): completed,
    # both 0 -> std 0.0. [20,25): node-1 deregistered, only node-2's single value
    # remains -> std 0.0. (0.5*10 + 0.0*10 + 0.0*5) / 25 = 0.2 — if complete left
    # instance-a counted as running, this would be higher.
    assert metrics.cluster_std == pytest.approx(0.2)


def test_compute_metrics_reflects_delete_and_release() -> None:
    # Round1 review (test Must): delete (as opposed to complete) had no test
    # exercising its own release of the allocation.
    b = RecordBuilder()
    b.register_node(0, "node-1", {"cpu": "10"})
    b.create_task(0, "task-a", "instance-a", cpu="4")
    b.bind(0, "task-a", "instance-a", "node-1", {"cpu": "4"})
    b.delete(10, "task-a", "instance-a", "node-1", {"cpu": "4"})
    b.create_task(20, "task-b", "instance-b", cpu="0")  # marks the end of observation

    metrics = aggregate.compute_metrics(b.transitions)

    # (0.4*10 + 0.0*10) / 20 = 0.2 — if delete's release left the allocation in
    # place, this would be 0.4 instead.
    assert metrics.allocation_rate["cpu"] == pytest.approx(0.2)


def test_compute_metrics_unplaceable_free_misses_fragmentation_across_different_resources() -> None:
    # round2 review, test IMO (aggregate.py:267-291): _is_fragmentation_blocked
    # checks each requested resource's own per-node max independently. node-1
    # has free cpu but no free gpu, node-2 has free gpu but no free cpu, so no
    # single node can satisfy a task needing both at once (real fragmentation),
    # yet each resource's own max individually looks sufficient. The docstring
    # says this under-detects rather than over-detects, and no existing test
    # exercised a request spanning more than one resource to show that.
    b = RecordBuilder()
    b.register_node(0, "node-1", {"cpu": "10", "nvidia.com/gpu": "0"})
    b.register_node(0, "node-2", {"cpu": "0", "nvidia.com/gpu": "4"})
    b.create_task(5, "task-1", "instance-1", cpu="1", gpu="1")
    b.create_task(10, "task-2", "instance-2", cpu="0")  # marks the end of observation

    metrics = aggregate.compute_metrics(b.transitions)

    # Never falsely reports fragmentation where there is none; this real
    # fragmentation case is a documented false negative, not a crash or a
    # wrong-direction answer.
    assert metrics.unplaceable_free == {"cpu": 0.0, "nvidia.com/gpu": 0.0}


def test_compute_metrics_reports_zero_unplaceable_free_for_a_resource_it_measured_without_fragmentation() -> None:
    # round3 review, correctness Must 1: a record that watched "cpu" for a
    # while and never hit fragmentation measured unplaceable_free:cpu and got
    # 0 — that is a measured 0, not a missing measurement. Leaving the key out
    # made every downstream step (the diff, the average, the per-run table)
    # read it as "never measured".
    b = RecordBuilder()
    b.register_node(0, "node-1", {"cpu": "10"})
    b.create_task(0, "task-1", "instance-1", cpu="1")
    b.bind(5, "task-1", "instance-1", "node-1", {"cpu": "1"})
    b.create_task(10, "task-2", "instance-2", cpu="0")  # marks the end of observation

    metrics = aggregate.compute_metrics(b.transitions)

    assert metrics.unplaceable_free == {"cpu": 0.0}


def test_compute_metrics_leaves_unplaceable_free_empty_for_a_record_with_no_interval_to_measure() -> None:
    # The flip side of the test above: a record with no positive-duration
    # interval (here: no transitions at all, units/U04.md "着手時に決めること")
    # measured nothing, so the key really is absent rather than 0.
    metrics = aggregate.compute_metrics([])

    assert metrics.unplaceable_free == {}


# --- DiffResult ---


def test_diff_result_rejects_neither_raw_nor_ci_set() -> None:
    # Round1 review, maintainability IMO: the "exactly one of raw/ci" invariant
    # was only a docstring promise; construction must enforce it.
    with pytest.raises(ValueError, match="exactly one"):
        aggregate.DiffResult(raw=None, ci=None)


def test_diff_result_rejects_both_raw_and_ci_set() -> None:
    with pytest.raises(ValueError, match="exactly one"):
        aggregate.DiffResult(raw=1.0, ci=(0.0, 2.0))


# --- bootstrap_diff ---


def test_bootstrap_diff_single_version_returns_the_raw_diff() -> None:
    result = aggregate.bootstrap_diff([4.0], seed=0)

    assert result.raw == pytest.approx(4.0)
    assert result.ci is None


def test_bootstrap_diff_constant_diffs_has_a_degenerate_ci_at_that_value() -> None:
    # Every bootstrap resample of a constant list has the same mean, so the 95% CI
    # collapses to that value regardless of the seed. This gives an expected value
    # that does not depend on how the resampling itself works.
    result = aggregate.bootstrap_diff([3.0, 3.0, 3.0, 3.0], seed=123)

    assert result.raw is None
    assert result.ci == pytest.approx((3.0, 3.0))


def test_bootstrap_diff_same_seed_is_reproducible() -> None:
    diffs = [1.0, 2.0, 3.0, 10.0]

    first = aggregate.bootstrap_diff(diffs, seed=42)
    second = aggregate.bootstrap_diff(diffs, seed=42)

    assert first.ci == second.ci


def test_bootstrap_diff_result_does_not_depend_on_other_calls_sharing_a_seed() -> None:
    # A single bootstrap_diff call must give the same answer whether or not some
    # other metric was bootstrapped first with the same seed (rihib:interface-judge
    # round 1, Must 1): each call builds its own generator from the seed, so calls
    # never share mutable random state.
    diffs = [1.0, 2.0, 3.0, 10.0]

    alone = aggregate.bootstrap_diff(diffs, seed=7)
    aggregate.bootstrap_diff([100.0, -50.0, 3.0], seed=7)  # an unrelated call first
    after_other_call = aggregate.bootstrap_diff(diffs, seed=7)

    assert alone.ci == after_other_call.ci


# --- average_metrics ---


def _metrics(**overrides) -> aggregate.Metrics:
    base = {
        "pending_wait_by_class": {},
        "pending_count_at_end": 0,
        "allocation_rate": {},
        "unplaceable_free": {},
        "cluster_std": 0.0,
        "nonzero_wait_fraction": 0.0,
    }
    base.update(overrides)
    return aggregate.Metrics(**base)


def test_average_metrics_averages_scalars_across_iterations() -> None:
    iterations = [
        _metrics(
            pending_count_at_end=2, cluster_std=1.0, nonzero_wait_fraction=0.5, allocation_rate={"cpu": 0.4}, unplaceable_free={"cpu": 10.0}
        ),
        _metrics(
            pending_count_at_end=4, cluster_std=3.0, nonzero_wait_fraction=1.0, allocation_rate={"cpu": 0.6}, unplaceable_free={"cpu": 30.0}
        ),
    ]

    avg = aggregate.average_metrics(iterations)

    assert avg.pending_count_at_end == pytest.approx(3.0)
    assert avg.cluster_std == pytest.approx(2.0)
    assert avg.nonzero_wait_fraction == pytest.approx(0.75)
    assert avg.allocation_rate["cpu"] == pytest.approx(0.5)
    assert avg.unplaceable_free["cpu"] == pytest.approx(20.0)


def test_average_metrics_averages_cluster_std_skipping_iterations_with_no_data() -> None:
    # round2 review, test IMO: _mean_optional's "some None, some present" branch
    # (the case this fix mainly protects — an iteration with no registered-node
    # interval mixed with one that has data) was never exercised.
    iterations = [_metrics(cluster_std=None), _metrics(cluster_std=1.0)]

    avg = aggregate.average_metrics(iterations)

    assert avg.cluster_std == pytest.approx(1.0)


def test_average_metrics_averages_a_resource_only_over_iterations_that_measured_it() -> None:
    # round2 review, correctness Must (aggregate.py:514-519): allocation_rate
    # and unplaceable_free must not default a missing resource to 0.0 when
    # averaging across iterations — an iteration whose record never saw "cpu"
    # (e.g. no transitions at all) must be excluded from cpu's average, not
    # folded in as a measured 0.
    iterations = [
        _metrics(allocation_rate={"cpu": 0.4}, unplaceable_free={"cpu": 10.0}),
        _metrics(allocation_rate={}, unplaceable_free={}),  # never measured cpu
    ]

    avg = aggregate.average_metrics(iterations)

    assert avg.allocation_rate["cpu"] == pytest.approx(0.4)
    assert avg.unplaceable_free["cpu"] == pytest.approx(10.0)


def test_average_metrics_averages_unplaceable_free_over_every_iteration_that_measured_it() -> None:
    # round3 review, correctness Must 1: an iteration that watched cpu and saw
    # no fragmentation measured 0, so the version's average must be divided by
    # both iterations (100 and 0 -> 50). Skipping the calm iteration reported
    # 100 — twice the fragmentation this version actually showed.
    iterations = [
        aggregate.compute_metrics(fragmented_record().transitions),
        aggregate.compute_metrics(unfragmented_record().transitions),
    ]

    avg = aggregate.average_metrics(iterations)

    assert avg.unplaceable_free["cpu"] == pytest.approx(50.0)


def test_average_metrics_keeps_a_resource_no_iteration_could_measure_out_of_the_average() -> None:
    # round3 review, test IMO 2: the "no iteration measured this resource at
    # all" guard. A node that declares a resource with capacity 0 puts it in
    # unplaceable_free (resource_names includes it) but never in
    # allocation_rate (_MetricsAccumulator skips a resource whose total
    # capacity is 0), so allocation_rate must come back without the key rather
    # than with a None value.
    b = RecordBuilder()
    b.register_node(0, "node-1", {"cpu": "5", "nvidia.com/gpu": "0"})
    b.register_node(0, "node-2", {"cpu": "5", "nvidia.com/gpu": "0"})
    b.create_task(0, "task-1", "instance-1", cpu="8")
    b.create_task(10, "task-2", "instance-2", cpu="0")  # marks the end of observation

    avg = aggregate.average_metrics([aggregate.compute_metrics(b.transitions)])

    assert "nvidia.com/gpu" not in avg.allocation_rate
    assert avg.allocation_rate["cpu"] == pytest.approx(0.0)
    assert avg.unplaceable_free["nvidia.com/gpu"] == pytest.approx(0.0)


def test_mean_optional_skips_none_values() -> None:
    assert aggregate._mean_optional([None, 2.0, None, 4.0]) == pytest.approx(3.0)


def test_mean_optional_returns_none_when_every_value_is_none() -> None:
    assert aggregate._mean_optional([None, None]) is None


def test_format_optional_renders_n_a_for_none() -> None:
    assert aggregate._format_optional(None) == "n/a"


def test_format_optional_renders_a_measured_value() -> None:
    assert aggregate._format_optional(0.0) == "0.0000"


def test_average_metrics_concatenates_wait_times_and_means_them_per_class() -> None:
    iterations = [
        _metrics(pending_wait_by_class={"0": [1.0, 3.0]}),
        _metrics(pending_wait_by_class={"0": [5.0]}),
    ]

    avg = aggregate.average_metrics(iterations)

    assert avg.pending_wait_by_class == {"0": [1.0, 3.0, 5.0]}
    assert avg.mean_wait_by_class["0"] == pytest.approx(3.0)


# --- build_rho_sections ---


def make_run_record(scheduler_profile: str, rho: float, transitions, *, input_seed="seed-1", iteration=0) -> aggregate.RunRecord:
    header = aggregate.RunHeader(
        evaluation_id="eval-1",
        scenario_id="scenario-1",
        iteration=iteration,
        trace_name="openb",
        trace_version="v1",
        reference_commit="abc",
        input_seed=input_seed,
        rho=rho,
        evaluator_commit="def",
        hardware="test-machine",
        scheduler_profile=scheduler_profile,
    )
    return aggregate.RunRecord(header=header, transitions=transitions)


def test_build_rho_sections_groups_by_rho_and_orders_least_allocated_first() -> None:
    least = RecordBuilder()
    least.create_task(0, "task-1", "instance-1", gpu="0")
    least.bind(10, "task-1", "instance-1", "node-1", {"cpu": "1"})
    most = RecordBuilder()
    most.create_task(0, "task-1", "instance-1", gpu="0")
    most.bind(30, "task-1", "instance-1", "node-1", {"cpu": "1"})

    records = [
        make_run_record("MostAllocated", 1.1, most.transitions),
        make_run_record("LeastAllocated", 1.1, least.transitions),
    ]

    sections = aggregate.build_rho_sections(records, seed=0)

    assert len(sections) == 1
    section = sections[0]
    assert section.rho == 1.1
    assert section.scheduler_order == ("LeastAllocated", "MostAllocated")
    # LeastAllocated's wait (10s) minus MostAllocated's wait (30s) is -20, a single
    # version so the diff is shown raw, not as a bootstrap CI (decisions.md).
    assert section.diffs["wait:0"].raw == pytest.approx(-20.0)
    assert section.diffs["wait:0"].ci is None


def test_build_rho_sections_separates_different_rho_values() -> None:
    a = RecordBuilder()
    a.create_task(0, "task-1", "instance-1")
    b = RecordBuilder()
    b.create_task(0, "task-1", "instance-1")

    records = [
        make_run_record("LeastAllocated", 0.5, a.transitions),
        make_run_record("LeastAllocated", 1.1, b.transitions),
    ]

    sections = aggregate.build_rho_sections(records, seed=0)

    assert sorted(s.rho for s in sections) == [0.5, 1.1]


def test_build_rho_sections_bootstraps_a_ci_across_two_input_versions() -> None:
    # Round1 review, test IMO: the version-grouping step that feeds bootstrap_diff
    # (as opposed to bootstrap_diff itself, which is tested directly with multiple
    # values) was only ever exercised with a single input version.
    least_v1 = RecordBuilder()
    least_v1.create_task(0, "task-1", "instance-1", gpu="0")
    least_v1.bind(10, "task-1", "instance-1", "node-1", {"cpu": "1"})
    most_v1 = RecordBuilder()
    most_v1.create_task(0, "task-1", "instance-1", gpu="0")
    most_v1.bind(30, "task-1", "instance-1", "node-1", {"cpu": "1"})
    least_v2 = RecordBuilder()
    least_v2.create_task(0, "task-1", "instance-1", gpu="0")
    least_v2.bind(20, "task-1", "instance-1", "node-1", {"cpu": "1"})
    most_v2 = RecordBuilder()
    most_v2.create_task(0, "task-1", "instance-1", gpu="0")
    most_v2.bind(50, "task-1", "instance-1", "node-1", {"cpu": "1"})

    records = [
        make_run_record("LeastAllocated", 1.1, least_v1.transitions, input_seed="seed-1"),
        make_run_record("MostAllocated", 1.1, most_v1.transitions, input_seed="seed-1"),
        make_run_record("LeastAllocated", 1.1, least_v2.transitions, input_seed="seed-2"),
        make_run_record("MostAllocated", 1.1, most_v2.transitions, input_seed="seed-2"),
    ]

    section = aggregate.build_rho_sections(records, seed=0)[0]

    # version seed-1's diff is 10-30=-20, seed-2's is 20-50=-30: two versions, so
    # the result is a bootstrap CI, not a raw value.
    assert section.diffs["wait:0"].raw is None
    assert section.diffs["wait:0"].ci is not None


def test_build_rho_sections_skips_a_version_missing_one_scheduler() -> None:
    # Round1 review, test IMO: the continue branch for "this version doesn't have
    # both schedulers" (e.g. one record was dropped as truncated) was never
    # exercised by any existing test.
    least_v1 = RecordBuilder()
    least_v1.create_task(0, "task-1", "instance-1", gpu="0")
    least_v1.bind(10, "task-1", "instance-1", "node-1", {"cpu": "1"})
    most_v1 = RecordBuilder()
    most_v1.create_task(0, "task-1", "instance-1", gpu="0")
    most_v1.bind(30, "task-1", "instance-1", "node-1", {"cpu": "1"})
    least_v2_only = RecordBuilder()  # seed-2 has no MostAllocated record at all
    least_v2_only.create_task(0, "task-1", "instance-1", gpu="0")
    least_v2_only.bind(20, "task-1", "instance-1", "node-1", {"cpu": "1"})

    records = [
        make_run_record("LeastAllocated", 1.1, least_v1.transitions, input_seed="seed-1"),
        make_run_record("MostAllocated", 1.1, most_v1.transitions, input_seed="seed-1"),
        make_run_record("LeastAllocated", 1.1, least_v2_only.transitions, input_seed="seed-2"),
    ]

    section = aggregate.build_rho_sections(records, seed=0)[0]

    # Only seed-1 contributed a diff (10-30=-20); seed-2 is skipped, so this is
    # still a single-version raw value, not a bootstrap CI.
    assert section.diffs["wait:0"].raw == pytest.approx(-20.0)
    assert section.diffs["wait:0"].ci is None


def test_build_rho_sections_uses_the_given_distributed_scheduler() -> None:
    # Round1 review, Must: the scheduler sorted first (and compared against in
    # the ρ = 1.1 prediction) must come from the caller, not a hardcoded
    # "LeastAllocated" string literal.
    tetris = RecordBuilder()
    tetris.create_task(0, "task-1", "instance-1", gpu="0")
    tetris.bind(10, "task-1", "instance-1", "node-1", {"cpu": "1"})
    most = RecordBuilder()
    most.create_task(0, "task-1", "instance-1", gpu="0")
    most.bind(30, "task-1", "instance-1", "node-1", {"cpu": "1"})

    records = [
        make_run_record("MostAllocated", 1.1, most.transitions),
        make_run_record("Tetris", 1.1, tetris.transitions),
    ]

    section = aggregate.build_rho_sections(records, seed=0, distributed_scheduler="Tetris")[0]

    assert section.scheduler_order == ("Tetris", "MostAllocated")
    assert section.distributed_scheduler == "Tetris"


def test_build_rho_sections_excludes_a_metric_one_scheduler_never_measured_from_the_diff() -> None:
    # round2 review, correctness Must: a wait-time class one scheduler's run
    # never produced (it never requested that many GPUs) must not be diffed
    # against a silent default of 0 — that misrepresents "never measured" as
    # "measured and zero".
    least = RecordBuilder()
    least.create_task(0, "task-1", "instance-1", gpu="1")
    least.bind(5, "task-1", "instance-1", "node-1", {"cpu": "1"})
    most = RecordBuilder()
    most.create_task(0, "task-1", "instance-1", gpu="0")  # never requests a GPU

    records = [
        make_run_record("LeastAllocated", 0.8, least.transitions),
        make_run_record("MostAllocated", 0.8, most.transitions),
    ]

    section = aggregate.build_rho_sections(records, seed=0)[0]

    assert "wait:1" not in section.diffs  # MostAllocated never measured a "1" GPU-class wait


def test_build_rho_sections_does_not_treat_a_transitionless_record_as_measuring_zero() -> None:
    # round2 review, correctness Must: a record whose write was interrupted
    # right after the header (parses as complete, but has zero transitions,
    # per units/U04.md "着手時に決めること") must not silently count as having
    # measured 0 for metrics it never had a chance to produce — mixing in such
    # a record skewed scenarios 4-6's diffs (confirmed: wait:1's diff came out
    # as 4.0000 before this fix).
    least = RecordBuilder()
    least.create_task(0, "task-1", "instance-1", gpu="1")
    least.bind(5, "task-1", "instance-1", "node-1", {"cpu": "1"})
    most_crashed = RecordBuilder()  # no transitions at all

    records = [
        make_run_record("LeastAllocated", 0.8, least.transitions),
        make_run_record("MostAllocated", 0.8, most_crashed.transitions),
    ]

    section = aggregate.build_rho_sections(records, seed=0)[0]

    assert "wait:1" not in section.diffs


def test_build_rho_sections_diffs_unplaceable_free_when_only_one_scheduler_fragmented() -> None:
    # round3 review, correctness Must 1: both runs watched cpu, so both
    # measured 置けない空き資源の量 — one got 100, the other a genuine 0. The
    # diff scenario 6 asks for must stay in the table; dropping the whole row
    # because one side's dict had no key hid the very difference the
    # experiment is looking for.
    records = [
        make_run_record("LeastAllocated", 1.1, fragmented_record().transitions),
        make_run_record("MostAllocated", 1.1, unfragmented_record().transitions),
    ]

    section = aggregate.build_rho_sections(records, seed=0)[0]

    assert section.diffs["unplaceable_free:cpu"].raw == pytest.approx(100.0)


def test_build_rho_sections_leaves_diffs_empty_when_no_version_has_both_schedulers() -> None:
    # round2 review, correctness Must: two profiles can be found at a ρ overall
    # while never coexisting within the same input version (e.g. each version
    # only ran one of the two schedulers so far), so no version contributes a
    # diff and build_rho_sections legitimately returns an empty diffs dict even
    # though scheduler_order has exactly two entries.
    least_only = RecordBuilder()
    least_only.create_task(0, "task-1", "instance-1")
    least_only.bind(5, "task-1", "instance-1", "node-1", {"cpu": "1"})
    most_only = RecordBuilder()
    most_only.create_task(0, "task-1", "instance-1")
    most_only.bind(5, "task-1", "instance-1", "node-1", {"cpu": "1"})

    records = [
        make_run_record("LeastAllocated", 0.8, least_only.transitions, input_seed="seed-1"),
        make_run_record("MostAllocated", 0.8, most_only.transitions, input_seed="seed-2"),
    ]

    section = aggregate.build_rho_sections(records, seed=0)[0]

    assert section.scheduler_order == ("LeastAllocated", "MostAllocated")
    assert section.diffs == {}


# --- render_rho_markdown ---


def _two_scheduler_section(rho: float) -> aggregate.RhoSection:
    least = RecordBuilder()
    least.create_task(0, "task-1", "instance-1", gpu="8")
    least.bind(10, "task-1", "instance-1", "node-1", {"cpu": "1"})
    most = RecordBuilder()
    most.create_task(0, "task-1", "instance-1", gpu="8")
    most.bind(2, "task-1", "instance-1", "node-1", {"cpu": "1"})
    records = [
        make_run_record("LeastAllocated", rho, least.transitions),
        make_run_record("MostAllocated", rho, most.transitions),
    ]
    return aggregate.build_rho_sections(records, seed=0)[0]


def test_render_rho_markdown_includes_run_table_and_diffs() -> None:
    section = _two_scheduler_section(0.8)

    md = aggregate.render_rho_markdown(section)

    assert "ρ = 0.8" in md
    assert "LeastAllocated" in md
    assert "MostAllocated" in md
    assert "wait:8" in md


def test_render_rho_markdown_rho_0_5_includes_negative_control() -> None:
    section = _two_scheduler_section(0.5)

    md = aggregate.render_rho_markdown(section)

    assert "陰性対照" in md
    assert "待機時間が0でない" in md


def test_render_rho_markdown_rho_1_1_includes_prediction_and_result() -> None:
    section = _two_scheduler_section(1.1)

    md = aggregate.render_rho_markdown(section)

    assert "予測" in md
    assert "結果" in md
    # LeastAllocated waited 10s, MostAllocated waited 2s: LeastAllocated is bigger,
    # so the prediction (units/U04.md, experiment.md 3.1) held.
    assert "当たった" in md


def test_render_rho_markdown_per_run_table_includes_allocation_rate_and_unplaceable_free() -> None:
    # Round1 review, Must: scenario 4 needs all 5 per-run metrics, but the table
    # only ever showed pending_count_at_end / cluster_std / nonzero_wait_fraction.
    b = RecordBuilder()
    b.register_node(0, "node-1", {"cpu": "10"})
    b.create_task(0, "task-1", "instance-1", cpu="4")
    b.bind(5, "task-1", "instance-1", "node-1", {"cpu": "4"})
    records = [make_run_record("LeastAllocated", 0.8, b.transitions)]
    section = aggregate.build_rho_sections(records, seed=0)[0]

    md = aggregate.render_rho_markdown(section)

    assert "allocation_rate:cpu" in md
    assert "unplaceable_free:cpu" in md


def test_render_rho_markdown_per_run_table_shows_n_a_for_a_resource_this_run_never_measured() -> None:
    # round2 review, correctness IMO (round1 leftover, aggregate.py:688-689):
    # allocation_rate/unplaceable_free showed a measured 0.0000 for a resource
    # this particular run's record never had a node registering, instead of
    # "n/a" like cluster_std already does — indistinguishable from an actually
    # measured 0.
    with_cpu = RecordBuilder()
    with_cpu.register_node(0, "node-1", {"cpu": "10"})
    with_cpu.create_task(0, "task-1", "instance-1", cpu="1")
    with_cpu.bind(5, "task-1", "instance-1", "node-1", {"cpu": "1"})
    without_cpu_node = RecordBuilder()  # never registers a cpu-capable node
    without_cpu_node.register_node(0, "node-2", {"nvidia.com/gpu": "4"})
    without_cpu_node.create_task(0, "task-2", "instance-2", cpu="0", gpu="1")
    without_cpu_node.bind(5, "task-2", "instance-2", "node-2", {"nvidia.com/gpu": "1"})
    records = [
        make_run_record("LeastAllocated", 0.8, with_cpu.transitions),
        make_run_record("MostAllocated", 0.8, without_cpu_node.transitions),
    ]
    section = aggregate.build_rho_sections(records, seed=0)[0]

    md = aggregate.render_rho_markdown(section)

    most_row = next(line for line in md.splitlines() if line.startswith("| MostAllocated |"))
    assert "n/a" in most_row  # MostAllocated's record never registered a cpu resource


def _per_run_cell(md: str, scheduler_profile: str, column: str) -> str:
    """The per-run table's cell for one scheduler's row and one column."""
    lines = md.splitlines()
    header = next(line for line in lines if line.startswith("| scheduler |"))
    row = next(line for line in lines if line.startswith(f"| {scheduler_profile} |"))
    columns = [c.strip() for c in header.strip("|").split("|")]
    cells = [c.strip() for c in row.strip("|").split("|")]
    return cells[columns.index(column)]


def test_render_rho_markdown_per_run_table_shows_zero_for_a_resource_measured_without_fragmentation() -> None:
    # round3 review, correctness Must 1: ρ = 0.5 (the negative control) is
    # expected to have no fragmentation at all, so showing "n/a" there made
    # scenario 4's fifth per-run metric unreadable for a whole ρ — the run did
    # measure 置けない空き資源の量 and it was 0.
    records = [make_run_record("LeastAllocated", 0.5, unfragmented_record().transitions)]
    section = aggregate.build_rho_sections(records, seed=0)[0]

    md = aggregate.render_rho_markdown(section)

    assert _per_run_cell(md, "LeastAllocated", "unplaceable_free:cpu") == "0.0000"


def test_render_rho_markdown_per_run_table_shows_n_a_for_cluster_std_a_run_never_measured() -> None:
    # round3 review, test IMO 3 (half-unfixed from round2): the per-run table's
    # cluster_std cell goes through _format_optional too, and no test covered
    # that wiring — only _format_optional itself and the allocation_rate column.
    no_interval = RecordBuilder()
    no_interval.create_task(0, "task-1", "instance-1")  # a single instant: no interval to average over
    records = [make_run_record("LeastAllocated", 0.8, no_interval.transitions)]
    section = aggregate.build_rho_sections(records, seed=0)[0]

    md = aggregate.render_rho_markdown(section)

    assert section.runs[0][1].cluster_std is None
    assert _per_run_cell(md, "LeastAllocated", "cluster_std") == "n/a"


def test_render_rho_markdown_links_to_the_figure_for_this_rho() -> None:
    section = _two_scheduler_section(0.8)

    md = aggregate.render_rho_markdown(section)

    assert aggregate._figure_relpath(0.8) in md


def test_render_rho_markdown_gives_no_verdict_to_small_gpu_classes_at_high_load() -> None:
    # Round1 review (Must, upgraded from IMO): experiment.md 3.1's prediction is
    # about tasks with a large request; the 0 and 1 GPU classes must be shown
    # for context without a 当たった/当たらなかった verdict.
    least = RecordBuilder()
    least.create_task(0, "task-1", "instance-1", gpu="0")
    least.bind(10, "task-1", "instance-1", "node-1", {"cpu": "1"})
    least.create_task(0, "task-2", "instance-2", gpu="8")
    least.bind(10, "task-2", "instance-2", "node-1", {"cpu": "1"})
    most = RecordBuilder()
    most.create_task(0, "task-1", "instance-1", gpu="0")
    most.bind(2, "task-1", "instance-1", "node-1", {"cpu": "1"})
    most.create_task(0, "task-2", "instance-2", gpu="8")
    most.bind(2, "task-2", "instance-2", "node-1", {"cpu": "1"})
    records = [
        make_run_record("LeastAllocated", 1.1, least.transitions),
        make_run_record("MostAllocated", 1.1, most.transitions),
    ]
    section = aggregate.build_rho_sections(records, seed=0)[0]

    md = aggregate.render_rho_markdown(section)

    # Both "スケジューラの差" and "予測と結果" have a row starting "| wait:8 |"; the
    # prediction table's row is the one with a verdict column.
    large_class_verdict_line = next(
        line for line in md.splitlines() if line.startswith("| wait:8 |") and ("当たった" in line or "当たらなかった" in line)
    )
    assert "8.0000" in large_class_verdict_line
    assert "当たった" in large_class_verdict_line  # LeastAllocated waited 10s vs MostAllocated's 2s
    small_class_verdict_row_lines = [line for line in md.splitlines() if line.startswith("| wait:0 |") and "対象外" in line]
    assert len(small_class_verdict_row_lines) == 1
    assert "当たった" not in small_class_verdict_row_lines[0]
    assert "当たらなかった" not in small_class_verdict_row_lines[0]


def test_render_rho_markdown_shows_a_reason_when_there_are_not_exactly_two_schedulers() -> None:
    # Round1 review, Must: scheduler_order being None (not exactly two profiles)
    # silently dropped the diff section; it must say why instead.
    b = RecordBuilder()
    b.create_task(0, "task-1", "instance-1")
    b.bind(5, "task-1", "instance-1", "node-1", {"cpu": "1"})
    records = [make_run_record("LeastAllocated", 0.8, b.transitions)]
    section = aggregate.build_rho_sections(records, seed=0)[0]

    md = aggregate.render_rho_markdown(section)

    assert section.scheduler_order is None
    assert "LeastAllocated" in md  # the one profile that was found is named
    assert "1 個のスケジューラ" in md


def test_render_rho_markdown_shows_a_reason_when_no_version_has_both_schedulers() -> None:
    # round2 review, correctness Must: diffs can be empty even when exactly two
    # profiles were found at this ρ, if they never coexist within the same
    # input version (see test_build_rho_sections_leaves_diffs_empty_when_no_
    # version_has_both_schedulers). The diff table must say why instead of
    # rendering a header with no rows.
    least_only = RecordBuilder()
    least_only.create_task(0, "task-1", "instance-1")
    least_only.bind(5, "task-1", "instance-1", "node-1", {"cpu": "1"})
    most_only = RecordBuilder()
    most_only.create_task(0, "task-1", "instance-1")
    most_only.bind(5, "task-1", "instance-1", "node-1", {"cpu": "1"})
    records = [
        make_run_record("LeastAllocated", 0.8, least_only.transitions, input_seed="seed-1"),
        make_run_record("MostAllocated", 0.8, most_only.transitions, input_seed="seed-2"),
    ]
    section = aggregate.build_rho_sections(records, seed=0)[0]

    md = aggregate.render_rho_markdown(section)

    diff_section = md.split("### スケジューラの差")[1].split("### ")[0]
    assert "| metric | diff |" not in diff_section
    assert "そろわ" in diff_section


def test_render_rho_markdown_rho_1_1_shows_a_reason_when_no_version_has_both_schedulers() -> None:
    # round2 review, correctness Must: the same empty-diffs case as above, but
    # for the ρ = 1.1 prediction/result section, which must not render an
    # empty table either.
    least_only = RecordBuilder()
    least_only.create_task(0, "task-1", "instance-1")
    least_only.bind(5, "task-1", "instance-1", "node-1", {"cpu": "1"})
    most_only = RecordBuilder()
    most_only.create_task(0, "task-1", "instance-1")
    most_only.bind(5, "task-1", "instance-1", "node-1", {"cpu": "1"})
    records = [
        make_run_record("LeastAllocated", 1.1, least_only.transitions, input_seed="seed-1"),
        make_run_record("MostAllocated", 1.1, most_only.transitions, input_seed="seed-2"),
    ]
    section = aggregate.build_rho_sections(records, seed=0)[0]

    md = aggregate.render_rho_markdown(section)

    prediction_section = md.split("### 予測と結果（高負荷）")[1]
    assert "| metric |" not in prediction_section
    assert "そろわ" in prediction_section


def test_render_rho_markdown_rho_1_1_says_which_metrics_were_missing_when_the_versions_did_line_up() -> None:
    # round3 review, correctness Must 4 / maintainability Must: the ρ = 1.1
    # section filters the diff down to the keys its prediction is about
    # (wait: / unplaceable_free:), which can come out empty while the diff
    # itself has other metrics. Blaming "the two profiles never coexisted in
    # any input version" there told the reader the wrong reason: here both
    # schedulers ran the same version, and the diff table below shows it.
    least = RecordBuilder()  # no tasks at all, so no wait times to compare
    least.register_node(0, "node-1", {"cpu": "10"})
    least.register_node(10, "node-2", {"cpu": "1"})
    most = RecordBuilder()  # and a disjoint resource, so no unplaceable_free to compare
    most.register_node(0, "node-3", {"nvidia.com/gpu": "4"})
    most.register_node(10, "node-4", {"nvidia.com/gpu": "1"})
    records = [
        make_run_record("LeastAllocated", 1.1, least.transitions),
        make_run_record("MostAllocated", 1.1, most.transitions),
    ]
    section = aggregate.build_rho_sections(records, seed=0)[0]

    md = aggregate.render_rho_markdown(section)

    assert "pending_count_at_end" in md.split("### スケジューラの差")[1]  # the versions did line up
    prediction_section = md.split("### 予測と結果（高負荷）")[1]
    assert "| metric |" not in prediction_section
    assert "そろわ" not in prediction_section
    assert "予測の対象" in prediction_section


def test_render_rho_markdown_rho_1_1_section_names_a_missing_distributed_scheduler() -> None:
    # Round1 review, Must: when the configured distributed scheduler isn't one
    # of this ρ's profiles, the ρ = 1.1 section must say so instead of just
    # vanishing (as it did when the check was a literal "LeastAllocated" test).
    a = RecordBuilder()
    a.create_task(0, "task-1", "instance-1")
    a.bind(5, "task-1", "instance-1", "node-1", {"cpu": "1"})
    b = RecordBuilder()
    b.create_task(0, "task-1", "instance-1")
    b.bind(5, "task-1", "instance-1", "node-1", {"cpu": "1"})
    records = [
        make_run_record("Tetris", 1.1, a.transitions),
        make_run_record("MostAllocated", 1.1, b.transitions),
    ]
    section = aggregate.build_rho_sections(records, seed=0)[0]  # default distributed_scheduler="LeastAllocated"

    md = aggregate.render_rho_markdown(section)

    assert "予測と結果" in md
    assert "見つからない" in md


def test_render_rho_markdown_treats_a_rho_within_tolerance_as_the_negative_control() -> None:
    # Round1 review, Must: == on floats can miss a ρ that is meant to be 0.5 but
    # carries ordinary floating-point noise from upstream computation.
    b = RecordBuilder()
    b.create_task(0, "task-1", "instance-1")
    b.bind(5, "task-1", "instance-1", "node-1", {"cpu": "1"})
    records = [make_run_record("LeastAllocated", 0.5 + 1e-9, b.transitions)]
    section = aggregate.build_rho_sections(records, seed=0)[0]

    md = aggregate.render_rho_markdown(section)

    assert "陰性対照" in md


# --- write_report ---


class FakeReportWriter:
    def __init__(self) -> None:
        self.texts: dict[str, str] = {}
        self.blobs: dict[str, bytes] = {}

    def write_text(self, relpath: str, content: str) -> None:
        self.texts[relpath] = content

    def write_bytes(self, relpath: str, content: bytes) -> None:
        self.blobs[relpath] = content


def _run_record_text(scheduler_profile: str, rho: float, builder: RecordBuilder) -> str:
    header = sample_header(scheduler={"profile": scheduler_profile}, input={**sample_header()["input"], "rho": rho})
    return record_text(
        header,
        [
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
            for t in builder.transitions
        ],
    )


def test_write_report_writes_markdown_and_one_png_figure_per_rho() -> None:
    least = RecordBuilder()
    least.create_task(0, "task-1", "instance-1")
    least.bind(5, "task-1", "instance-1", "node-1", {"cpu": "1"})
    most = RecordBuilder()
    most.create_task(0, "task-1", "instance-1")
    most.bind(5, "task-1", "instance-1", "node-1", {"cpu": "1"})
    records = [
        make_run_record("LeastAllocated", 0.5, least.transitions),
        make_run_record("MostAllocated", 0.5, most.transitions),
    ]
    writer = FakeReportWriter()

    aggregate.write_report(records, writer, seed=0)

    assert "report.md" in writer.texts
    assert "ρ = 0.5" in writer.texts["report.md"]
    assert len(writer.blobs) == 1
    png_bytes = next(iter(writer.blobs.values()))
    assert png_bytes.startswith(b"\x89PNG\r\n\x1a\n")  # the PNG magic number


def test_write_report_notes_the_fragmentation_approximation() -> None:
    # round2 review, correctness IMO (aggregate.py:267-289): unplaceable_free's
    # per-resource-max approximation can miss real multi-resource
    # fragmentation; the report must say so instead of presenting the number
    # as an exact measurement.
    b = RecordBuilder()
    b.create_task(0, "task-1", "instance-1")
    b.bind(5, "task-1", "instance-1", "node-1", {"cpu": "1"})
    records = [make_run_record("LeastAllocated", 0.8, b.transitions)]
    writer = FakeReportWriter()

    aggregate.write_report(records, writer, seed=0)

    assert "近似" in writer.texts["report.md"]


# --- run (the CLI entry) ---


def test_run_reads_and_writes_via_injected_fakes() -> None:
    least = RecordBuilder()
    least.create_task(0, "task-1", "instance-1")
    least.bind(5, "task-1", "instance-1", "node-1", {"cpu": "1"})
    texts = [(Path("run-a.jsonl"), _run_record_text("LeastAllocated", 0.5, least))]
    writer = FakeReportWriter()

    code = aggregate.run(
        ["--records-dir", "unused", "--output-dir", "unused", "--seed", "0"],
        read_record_texts=lambda _: texts,
        writer_factory=lambda _: writer,
    )

    assert code == 0
    assert "report.md" in writer.texts


def test_run_fails_when_read_record_texts_raises() -> None:
    # OSError (a real I/O failure, e.g. permission denied) is the error a
    # read_record_texts adapter actually raises for "could not read the
    # records" (round2 review, correctness IMO: the catch used to be
    # `except Exception`, which also hid bugs inside the adapter itself).
    def failing_read(_):
        raise OSError("boom")

    code = aggregate.run(
        ["--records-dir", "unused", "--output-dir", "unused"],
        read_record_texts=failing_read,
        writer_factory=lambda _: FakeReportWriter(),
    )

    assert code == 1


def test_run_lets_a_non_os_error_from_read_record_texts_propagate() -> None:
    # round2 review, correctness IMO (aggregate.py:880): a wide
    # `except Exception` around reading records turned a bug inside
    # read_record_texts into a misleading "failed to read records" message
    # with no traceback. Only OSError is caught; anything else must crash
    # visibly instead.
    def buggy_read(_):
        raise TypeError("not a real I/O failure")

    with pytest.raises(TypeError):
        aggregate.run(
            ["--records-dir", "unused", "--output-dir", "unused"],
            read_record_texts=buggy_read,
            writer_factory=lambda _: FakeReportWriter(),
        )


def test_run_lets_a_non_parsing_error_from_parse_record_propagate(monkeypatch: pytest.MonkeyPatch) -> None:
    # round2 review, correctness IMO (aggregate.py:891): same narrowing on the
    # parse side — parse_record's own bugs (not a malformed-input
    # ValueError/KeyError/JSONDecodeError) must crash visibly instead of being
    # relabeled as "failed to parse <file>".
    def buggy_parse(_text):
        raise TypeError("not a parsing failure")

    monkeypatch.setattr(aggregate, "parse_record", buggy_parse)

    with pytest.raises(TypeError):
        aggregate.run(
            ["--records-dir", "unused", "--output-dir", "unused"],
            read_record_texts=lambda _: [(Path("a.jsonl"), "irrelevant")],
            writer_factory=lambda _: FakeReportWriter(),
        )


def test_run_fails_when_no_records_found() -> None:
    code = aggregate.run(
        ["--records-dir", "unused", "--output-dir", "unused"],
        read_record_texts=lambda _: [],
        writer_factory=lambda _: FakeReportWriter(),
    )

    assert code == 1


def test_run_fails_when_a_record_fails_to_parse() -> None:
    code = aggregate.run(
        ["--records-dir", "unused", "--output-dir", "unused"],
        read_record_texts=lambda _: [(Path("bad.jsonl"), "not json at all")],
        writer_factory=lambda _: FakeReportWriter(),
    )

    assert code == 1


def test_run_skips_a_truncated_record_and_still_succeeds_with_the_rest() -> None:
    least = RecordBuilder()
    least.create_task(0, "task-1", "instance-1")
    least.bind(5, "task-1", "instance-1", "node-1", {"cpu": "1"})
    most = RecordBuilder()
    most.create_task(0, "task-1", "instance-1")
    most.bind(5, "task-1", "instance-1", "node-1", {"cpu": "1"})
    good_texts = [
        (Path("a.jsonl"), _run_record_text("LeastAllocated", 0.5, least)),
        (Path("b.jsonl"), _run_record_text("MostAllocated", 0.5, most)),
    ]
    truncated_text = (Path("c.jsonl"), good_texts[0][1] + '{"time": "2024-01-01T00:00:20')  # cut off mid-line
    writer = FakeReportWriter()

    code = aggregate.run(
        ["--records-dir", "unused", "--output-dir", "unused"],
        read_record_texts=lambda _: [*good_texts, truncated_text],
        writer_factory=lambda _: writer,
    )

    assert code == 0
    assert "report.md" in writer.texts


def test_run_skips_a_record_with_no_transitions_and_warns(capsys: pytest.CaptureFixture[str]) -> None:
    # round3 review, correctness Must 2: a record cut off right after its
    # header parses as complete (truncated is False) but measured nothing, so
    # its pending_count_at_end of 0 is not a measurement — diffed against a
    # scheduler that left 4 tasks pending it reported a 4.0000 difference
    # between the two schedulers that no run ever observed. run() drops such a
    # record the same way it drops a truncated one, and says so.
    least = RecordBuilder()
    for i in range(4):
        least.create_task(0, f"task-{i}", f"instance-{i}")  # all four stay pending to the end
    least.create_task(10, "task-placed", "instance-placed")
    least.bind(10, "task-placed", "instance-placed", "node-1", {"cpu": "1"})
    header_only = RecordBuilder()  # the write was interrupted right after the header
    texts = [
        (Path("a.jsonl"), _run_record_text("LeastAllocated", 1.1, least)),
        (Path("b.jsonl"), _run_record_text("MostAllocated", 1.1, header_only)),
    ]
    writer = FakeReportWriter()

    code = aggregate.run(
        ["--records-dir", "unused", "--output-dir", "unused"],
        read_record_texts=lambda _: texts,
        writer_factory=lambda _: writer,
    )

    assert code == 0
    assert "遷移" in capsys.readouterr().err  # the reader is told one record was skipped
    report = writer.texts["report.md"]
    assert "1 個のスケジューラ" in report  # only LeastAllocated is left at this ρ
    assert "pending_count_at_end" not in report.split("### スケジューラの差")[1]


def test_run_fails_when_every_record_has_no_transitions() -> None:
    # round3 review, correctness Must 2: with nothing but header-only records
    # there is nothing to aggregate, which must be an error rather than an
    # empty report.
    header_only = RecordBuilder()
    texts = [(Path("a.jsonl"), _run_record_text("LeastAllocated", 0.5, header_only))]

    code = aggregate.run(
        ["--records-dir", "unused", "--output-dir", "unused"],
        read_record_texts=lambda _: texts,
        writer_factory=lambda _: FakeReportWriter(),
    )

    assert code == 1


def test_run_fails_when_every_record_is_truncated() -> None:
    least = RecordBuilder()
    least.create_task(0, "task-1", "instance-1")
    only_truncated = (Path("a.jsonl"), _run_record_text("LeastAllocated", 0.5, least) + '{"time": "2024-01-01T00:00:20')

    code = aggregate.run(
        ["--records-dir", "unused", "--output-dir", "unused"],
        read_record_texts=lambda _: [only_truncated],
        writer_factory=lambda _: FakeReportWriter(),
    )

    assert code == 1


# --- integration tests: real folders, real adapters ---


@pytest.mark.integration
def test_run_writes_report_and_figures_to_real_output_dir(tmp_path: Path) -> None:
    records_dir = tmp_path / "records"
    records_dir.mkdir()
    output_dir = tmp_path / "out"
    b = RecordBuilder()
    b.create_task(0, "task-1", "instance-1")
    b.bind(5, "task-1", "instance-1", "node-1", {"cpu": "1"})
    (records_dir / "run-a.jsonl").write_text(_run_record_text("LeastAllocated", 0.5, b))

    code = aggregate.run(["--records-dir", str(records_dir), "--output-dir", str(output_dir)])

    assert code == 0
    assert (output_dir / "report.md").exists()
    assert "ρ = 0.5" in (output_dir / "report.md").read_text(encoding="utf-8")
    assert list((output_dir / "figures").glob("*.png"))


@pytest.mark.integration
def test_run_fails_when_records_dir_has_corrupt_jsonl(tmp_path: Path) -> None:
    records_dir = tmp_path / "records"
    records_dir.mkdir()
    (records_dir / "broken.jsonl").write_text("{not valid json\n")
    output_dir = tmp_path / "out"

    code = aggregate.run(["--records-dir", str(records_dir), "--output-dir", str(output_dir)])

    assert code == 1


@pytest.mark.integration
def test_run_fails_when_a_record_file_is_not_utf8(tmp_path: Path) -> None:
    # round3 review, correctness IMO: a record file that is not valid UTF-8 is
    # a corrupt record, an expected failure of reading. read_text raises
    # UnicodeDecodeError, which is a ValueError and not an OSError, so the
    # narrowed except missed it and the command died with a traceback that
    # never named the file.
    records_dir = tmp_path / "records"
    records_dir.mkdir()
    (records_dir / "binary.jsonl").write_bytes(b'{"evaluation_id": "\xff\xfe"}\n')
    output_dir = tmp_path / "out"

    code = aggregate.run(["--records-dir", str(records_dir), "--output-dir", str(output_dir)])

    assert code == 1


@pytest.mark.integration
def test_run_skips_a_record_truncated_on_disk_and_still_succeeds(tmp_path: Path) -> None:
    records_dir = tmp_path / "records"
    records_dir.mkdir()
    output_dir = tmp_path / "out"
    good = RecordBuilder()
    good.create_task(0, "task-1", "instance-1")
    good.bind(5, "task-1", "instance-1", "node-1", {"cpu": "1"})
    (records_dir / "good.jsonl").write_text(_run_record_text("LeastAllocated", 0.5, good))
    (records_dir / "cut-off.jsonl").write_text(_run_record_text("MostAllocated", 0.5, good) + '{"time": "2024-01-01T00:00:20')

    code = aggregate.run(["--records-dir", str(records_dir), "--output-dir", str(output_dir)])

    assert code == 0
    assert (output_dir / "report.md").exists()


@pytest.mark.integration
def test_run_fails_when_records_dir_is_missing(tmp_path: Path) -> None:
    output_dir = tmp_path / "out"

    code = aggregate.run(["--records-dir", str(tmp_path / "does-not-exist"), "--output-dir", str(output_dir)])

    assert code == 1


if __name__ == "__main__":
    raise SystemExit(pytest.main([__file__]))

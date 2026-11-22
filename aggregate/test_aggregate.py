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

import numpy as np
import pytest

sys.path.insert(0, str(Path(__file__).parent))
import aggregate


def write_record_file(path: Path, header: dict, transitions: list[dict]) -> None:
    with path.open("w") as f:
        f.write(json.dumps(header) + "\n")
        for t in transitions:
            f.write(json.dumps(t) + "\n")


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


def test_read_record_file_returns_header_fields(tmp_path: Path) -> None:
    path = tmp_path / "run.jsonl"
    write_record_file(path, sample_header(), [])

    record = aggregate.read_record_file(path)

    assert record.header.trace_name == "openb"
    assert record.header.trace_version == "v1"
    assert record.header.input_seed == "seed-1"
    assert record.header.rho == 0.5
    assert record.header.scheduler_profile == "LeastAllocated"
    assert record.header.iteration == 0


def test_read_record_file_returns_transitions_in_order(tmp_path: Path) -> None:
    path = tmp_path / "run.jsonl"
    transitions = [
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
    write_record_file(path, sample_header(), transitions)

    record = aggregate.read_record_file(path)

    assert len(record.transitions) == 2
    assert record.transitions[0].operation == "create_task"
    assert record.transitions[0].seq == 1
    assert record.transitions[0].task_immutable["owner"] == "u1"
    assert record.transitions[1].operation == "assign_node"
    assert record.transitions[1].after == "node-1"


def test_read_records_from_folder_reads_every_jsonl_file(tmp_path: Path) -> None:
    write_record_file(tmp_path / "run-a.jsonl", sample_header(scheduler={"profile": "LeastAllocated"}), [])
    write_record_file(tmp_path / "run-b.jsonl", sample_header(scheduler={"profile": "MostAllocated"}), [])
    (tmp_path / "not-a-record.txt").write_text("ignore me")

    records = aggregate.read_records_from_folder(tmp_path)

    profiles = sorted(r.header.scheduler_profile for r in records)
    assert profiles == ["LeastAllocated", "MostAllocated"]


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


def test_compute_metrics_pending_wait_by_gpu_class_and_count_at_end() -> None:
    b = RecordBuilder()
    b.create_task(0, "task-1", "instance-1", gpu="0")
    b.create_task(0, "task-2", "instance-2", gpu="1")
    b.create_task(2, "task-3", "instance-3", gpu="3")
    b.bind(5, "task-1", "instance-1", "node-1", {"cpu": "1"})
    b.bind(12, "task-3", "instance-3", "node-1", {"cpu": "1"})
    b.bind(15, "task-2", "instance-2", "node-1", {"cpu": "1"})
    b.create_task(20, "task-4", "instance-4", gpu="0")  # stays pending
    b.create_task(20, "task-5", "instance-5", gpu="0")
    b.bind(20, "task-5", "instance-5", "node-1", {"cpu": "1"})  # wait == 0

    metrics = aggregate.compute_metrics(b.transitions)

    assert metrics.pending_wait_by_class == {
        "0": [5.0, 0.0],
        "1": [15.0],
        "2-4": [10.0],
    }
    assert metrics.pending_count_at_end == 1
    assert metrics.nonzero_wait_fraction == pytest.approx(3 / 4)


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


# --- bootstrap_diff ---


def test_bootstrap_diff_single_version_returns_the_raw_diff() -> None:
    result = aggregate.bootstrap_diff([4.0], rng=np.random.default_rng(0))

    assert result.raw == pytest.approx(4.0)
    assert result.ci is None


def test_bootstrap_diff_constant_diffs_has_a_degenerate_ci_at_that_value() -> None:
    # Every bootstrap resample of a constant list has the same mean, so the 95% CI
    # collapses to that value regardless of the seed. This gives an expected value
    # that does not depend on how the resampling itself works.
    result = aggregate.bootstrap_diff([3.0, 3.0, 3.0, 3.0], rng=np.random.default_rng(123))

    assert result.raw is None
    assert result.ci == pytest.approx((3.0, 3.0))


def test_bootstrap_diff_same_seed_is_reproducible() -> None:
    diffs = [1.0, 2.0, 3.0, 10.0]

    first = aggregate.bootstrap_diff(diffs, rng=np.random.default_rng(42))
    second = aggregate.bootstrap_diff(diffs, rng=np.random.default_rng(42))

    assert first.ci == second.ci


# --- average_metrics ---


def _metrics(**overrides) -> aggregate.Metrics:
    base = dict(
        pending_wait_by_class={},
        pending_count_at_end=0,
        allocation_rate={},
        unplaceable_free={},
        cluster_std=0.0,
        nonzero_wait_fraction=0.0,
    )
    base.update(overrides)
    return aggregate.Metrics(**base)


def test_average_metrics_averages_scalars_across_iterations() -> None:
    iterations = [
        _metrics(pending_count_at_end=2, cluster_std=1.0, nonzero_wait_fraction=0.5,
                  allocation_rate={"cpu": 0.4}, unplaceable_free={"cpu": 10.0}),
        _metrics(pending_count_at_end=4, cluster_std=3.0, nonzero_wait_fraction=1.0,
                  allocation_rate={"cpu": 0.6}, unplaceable_free={"cpu": 30.0}),
    ]

    avg = aggregate.average_metrics(iterations)

    assert avg.pending_count_at_end == pytest.approx(3.0)
    assert avg.cluster_std == pytest.approx(2.0)
    assert avg.nonzero_wait_fraction == pytest.approx(0.75)
    assert avg.allocation_rate["cpu"] == pytest.approx(0.5)
    assert avg.unplaceable_free["cpu"] == pytest.approx(20.0)


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

    sections = aggregate.build_rho_sections(records, rng=np.random.default_rng(0))

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

    sections = aggregate.build_rho_sections(records, rng=np.random.default_rng(0))

    assert sorted(s.rho for s in sections) == [0.5, 1.1]


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
    return aggregate.build_rho_sections(records, rng=np.random.default_rng(0))[0]


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


# --- write_report ---


class FakeReportWriter:
    def __init__(self) -> None:
        self.texts: dict[str, str] = {}
        self.figures: dict[str, object] = {}

    def write_text(self, relpath: str, content: str) -> None:
        self.texts[relpath] = content

    def write_figure(self, relpath: str, fig) -> None:
        self.figures[relpath] = fig


def test_write_report_writes_markdown_and_one_figure_per_rho() -> None:
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

    aggregate.write_report(records, writer, rng=np.random.default_rng(0))

    assert "report.md" in writer.texts
    assert "ρ = 0.5" in writer.texts["report.md"]
    assert len(writer.figures) == 1


# --- run (the CLI entry) ---


def test_run_reads_and_writes_via_injected_fakes() -> None:
    least = RecordBuilder()
    least.create_task(0, "task-1", "instance-1")
    least.bind(5, "task-1", "instance-1", "node-1", {"cpu": "1"})
    records = [make_run_record("LeastAllocated", 0.5, least.transitions)]
    writer = FakeReportWriter()

    code = aggregate.run(
        ["--records-dir", "unused", "--output-dir", "unused", "--seed", "0"],
        read_records=lambda _: records,
        writer_factory=lambda _: writer,
    )

    assert code == 0
    assert "report.md" in writer.texts


def test_run_fails_when_read_records_raises() -> None:
    def failing_read(_):
        raise ValueError("boom")

    code = aggregate.run(
        ["--records-dir", "unused", "--output-dir", "unused"],
        read_records=failing_read,
        writer_factory=lambda _: FakeReportWriter(),
    )

    assert code == 1


def test_run_fails_when_no_records_found() -> None:
    code = aggregate.run(
        ["--records-dir", "unused", "--output-dir", "unused"],
        read_records=lambda _: [],
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
    write_record_file(
        records_dir / "run-a.jsonl",
        sample_header(scheduler={"profile": "LeastAllocated"}),  # rho defaults to 0.5
        [
            {
                "time": t.time, "seq": t.seq, "operation": t.operation, "component_id": t.component_id,
                "task_id": t.task_id, "state": t.state, "state_key": t.state_key, "before": t.before,
                "after": t.after, "task_immutable": t.task_immutable,
            }
            for t in b.transitions
        ],
    )

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
def test_run_fails_when_records_dir_is_missing(tmp_path: Path) -> None:
    output_dir = tmp_path / "out"

    code = aggregate.run(["--records-dir", str(tmp_path / "does-not-exist"), "--output-dir", str(output_dir)])

    assert code == 1


if __name__ == "__main__":
    raise SystemExit(pytest.main([__file__]))

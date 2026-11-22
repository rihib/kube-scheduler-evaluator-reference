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
import aggregate  # noqa: E402


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

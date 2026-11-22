# /// script
# requires-python = ">=3.12"
# dependencies = [
#   "numpy",
#   "pandas",
#   "matplotlib",
# ]
# ///
"""Aggregates kube-scheduler-evaluator transition records into experiment 1's
metrics and writes a Markdown report.

Record format: see kube-scheduler-evaluator's README.md "Transition record" section
and internal/record/record.go.
"""

from __future__ import annotations

import json
from dataclasses import dataclass
from pathlib import Path


@dataclass(frozen=True)
class Transition:
    time: str
    seq: int
    operation: str
    component_id: str
    task_id: str | None
    state: str
    state_key: str | None
    before: object
    after: object
    task_immutable: dict | None


@dataclass(frozen=True)
class RunHeader:
    evaluation_id: str
    scenario_id: str
    iteration: int
    trace_name: str
    trace_version: str
    reference_commit: str
    input_seed: str
    rho: float
    evaluator_commit: str
    hardware: str
    scheduler_profile: str


@dataclass(frozen=True)
class RunRecord:
    header: RunHeader
    transitions: list[Transition]


def read_record_file(path: Path) -> RunRecord:
    """Reads one run's JSONL record file (header line + transition lines)."""
    return RunRecord(header=None, transitions=[])  # type: ignore[arg-type]

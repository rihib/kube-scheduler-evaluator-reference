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
    with path.open() as f:
        lines = [json.loads(line) for line in f if line.strip()]
    if not lines:
        raise ValueError(f"{path}: record file is empty, missing its header line")

    h = lines[0]
    header = RunHeader(
        evaluation_id=h["evaluation_id"],
        scenario_id=h["scenario_id"],
        iteration=h["iteration"],
        trace_name=h["input"]["trace_name"],
        trace_version=h["input"]["trace_version"],
        reference_commit=h["input"]["reference_commit"],
        input_seed=h["input"]["input_seed"],
        rho=h["input"]["rho"],
        evaluator_commit=h["execution"]["evaluator_commit"],
        hardware=h["execution"]["hardware"],
        scheduler_profile=h["scheduler"]["profile"],
    )

    transitions = [
        Transition(
            time=t["time"],
            seq=t["seq"],
            operation=t["operation"],
            component_id=t["component_id"],
            task_id=t.get("task_id"),
            state=t["state"],
            state_key=t.get("state_key"),
            before=t.get("before"),
            after=t.get("after"),
            task_immutable=t.get("task_immutable"),
        )
        for t in lines[1:]
    ]
    return RunRecord(header=header, transitions=transitions)

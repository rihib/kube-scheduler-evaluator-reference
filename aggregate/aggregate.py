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
import re
from dataclasses import dataclass
from datetime import datetime, timezone
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


@dataclass(frozen=True)
class Metrics:
    """One run's experiment 1 metrics (units/U04.md Implementation Decisions)."""

    # GPU request class ("0", "1", "2-4", "8") -> list of pending-state dwell times in
    # seconds, for instances that left pending via assign_node within the observation.
    pending_wait_by_class: dict[str, list[float]]
    # Instances still in the pending state at the end of the observation period.
    pending_count_at_end: int
    # Resource name -> time-weighted average of (allocated / capacity) over active nodes.
    allocation_rate: dict[str, float]
    # Resource name -> time integral (resource-seconds) of the cluster's free amount of
    # that resource during intervals where a pending task's request fits the cluster's
    # aggregate free resources but fits no single node (experiment.md 3.4).
    unplaceable_free: dict[str, float]
    # Time-weighted average of the population standard deviation of running-pod counts
    # across registered nodes (RBRA's Cluster_std, experiment.md 3.4 last row).
    cluster_std: float
    # Fraction of instances with wait data whose pending dwell time was nonzero.
    nonzero_wait_fraction: float


def compute_metrics(transitions: list[Transition]) -> Metrics:
    """Computes one run's experiment 1 metrics from its transition record."""
    return Metrics(
        pending_wait_by_class={},
        pending_count_at_end=0,
        allocation_rate={},
        unplaceable_free={},
        cluster_std=0.0,
        nonzero_wait_fraction=0.0,
    )


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


def read_records_from_folder(folder: Path) -> list[RunRecord]:
    """Reads every *.jsonl record file in folder. The real ReadRecords adapter."""
    return [read_record_file(p) for p in sorted(Path(folder).glob("*.jsonl"))]


_BINARY_SUFFIXES = {"Ki": 1024, "Mi": 1024**2, "Gi": 1024**3, "Ti": 1024**4, "Pi": 1024**5, "Ei": 1024**6}
_DECIMAL_SUFFIXES = {"n": 1e-9, "u": 1e-6, "m": 1e-3, "k": 1e3, "M": 1e6, "G": 1e9, "T": 1e12, "P": 1e15, "E": 1e18}


def parse_quantity(value: str) -> float:
    """Parses a Kubernetes resource quantity string (e.g. "500m", "1Gi") into a float
    in base units (cores, bytes, or GPU count)."""
    value = value.strip()
    for suffix, factor in _BINARY_SUFFIXES.items():
        if value.endswith(suffix):
            return float(value[: -len(suffix)]) * factor
    for suffix, factor in _DECIMAL_SUFFIXES.items():
        if value.endswith(suffix):
            return float(value[: -len(suffix)]) * factor
    return float(value)


def classify_gpu_class(gpu_count: float) -> str:
    """Classifies a task's GPU request into experiment 1's request classes
    (units/U04.md): 0, 1, 2-4, or 8+ GPUs."""
    if gpu_count <= 0:
        return "0"
    if gpu_count == 1:
        return "1"
    if gpu_count <= 4:
        return "2-4"
    return "8"


_VIRTUAL_TIME_RE = re.compile(
    r"^(?P<date>\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(?P<frac>\d+))?(?P<tz>Z|[+-]\d{2}:\d{2})$"
)


def parse_time(value: str) -> float:
    """Parses the evaluator's virtual time (record.go's RFC3339-nano format, which
    carries 9 fractional digits Go's own time parsing can hold but Python's
    datetime cannot) into seconds since the Unix epoch, as a float."""
    m = _VIRTUAL_TIME_RE.match(value)
    if not m:
        raise ValueError(f"not a virtual time: {value!r}")
    dt = datetime.strptime(m.group("date"), "%Y-%m-%dT%H:%M:%S").replace(tzinfo=timezone.utc)
    seconds = dt.timestamp()
    if m.group("frac"):
        seconds += float(f"0.{m.group('frac')}")
    tz = m.group("tz")
    if tz != "Z":
        sign = 1 if tz[0] == "+" else -1
        hh, mm = tz[1:].split(":")
        seconds -= sign * (int(hh) * 3600 + int(mm) * 60)
    return seconds

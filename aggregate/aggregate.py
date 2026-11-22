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

import copy
import json
import re
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path

import numpy as np

# Operation and mutable-state names, mirroring kube-scheduler-evaluator's
# internal/record/record.go so this module reads the same vocabulary it writes.
OpCreateTask = "create_task"
OpRecreate = "recreate"
OpAssignNode = "assign_node"
OpStart = "start"
OpComplete = "complete"
OpDelete = "delete"
OpRelease = "release"
OpRegisterNode = "register_node"
OpDeregisterNode = "deregister_node"

StateInstanceStatus = "instance_status"
StateAssignedNode = "assigned_node"
StateResourceCapacity = "resource_capacity"
StateAllocatedResource = "allocated_resource"


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


def _parsed_resources(raw: dict[str, str] | None) -> dict[str, float]:
    return {k: parse_quantity(v) for k, v in (raw or {}).items()}


class _World:
    """Mutable reconstruction of the cluster's state as of one point in virtual
    time, built by replaying a run's transitions in order."""

    def __init__(self) -> None:
        self.node_capacity: dict[str, dict[str, float]] = {}
        self.node_allocated: dict[str, dict[str, float]] = {}
        self.instance_status: dict[str, str] = {}
        self.instance_node: dict[str, str] = {}
        self.instance_task: dict[str, str] = {}
        self.instance_allocation: dict[str, tuple[str, dict[str, float]]] = {}
        self.task_request: dict[str, dict[str, float]] = {}
        self.pending_since: dict[str, float] = {}

    def apply(self, tr: Transition, time_val: float, on_wait=None) -> None:
        op, state = tr.operation, tr.state
        if op == OpRegisterNode:
            if state == StateResourceCapacity:
                self.node_capacity[tr.component_id] = _parsed_resources(tr.after)
                self.node_allocated.setdefault(tr.component_id, {})
        elif op == OpDeregisterNode:
            self.node_capacity.pop(tr.component_id, None)
            self.node_allocated.pop(tr.component_id, None)
        elif op in (OpCreateTask, OpRecreate):
            if state == StateInstanceStatus:
                self.instance_status[tr.component_id] = "pending"
                self.instance_task[tr.component_id] = tr.task_id
                self.pending_since[tr.component_id] = time_val
                if tr.task_immutable:
                    self.task_request[tr.task_id] = _parsed_resources(tr.task_immutable.get("resource_request"))
        elif op == OpAssignNode:
            if state == StateAssignedNode:
                self.instance_node[tr.component_id] = tr.after
            elif state == StateAllocatedResource:
                node_id, instance_id = tr.component_id, tr.state_key
                alloc = _parsed_resources(tr.after)
                allocated = self.node_allocated.setdefault(node_id, {})
                for r, v in alloc.items():
                    allocated[r] = allocated.get(r, 0.0) + v
                self.instance_allocation[instance_id] = (node_id, alloc)
        elif op == OpStart:
            instance_id = tr.component_id
            self.instance_status[instance_id] = "running"
            start = self.pending_since.pop(instance_id, None)
            if start is not None and on_wait is not None:
                on_wait(instance_id, self.instance_task.get(instance_id), time_val - start)
        elif op == OpComplete:
            self.instance_status[tr.component_id] = "completed"
        elif op == OpDelete:
            if state == StateInstanceStatus:
                self.instance_status[tr.component_id] = "deleted"
            elif state == StateAssignedNode:
                self.instance_node[tr.component_id] = None
        elif op == OpRelease:
            instance_id = tr.state_key
            entry = self.instance_allocation.pop(instance_id, None)
            if entry is not None:
                node_id, alloc = entry
                allocated = self.node_allocated.setdefault(node_id, {})
                for r, v in alloc.items():
                    allocated[r] = allocated.get(r, 0.0) - v

    def resource_names(self) -> set[str]:
        names: set[str] = set()
        for cap in self.node_capacity.values():
            names.update(cap.keys())
        return names

    def capacity_total(self, resource: str) -> float:
        return sum(cap.get(resource, 0.0) for cap in self.node_capacity.values())

    def allocated_total(self, resource: str) -> float:
        return sum(alloc.get(resource, 0.0) for alloc in self.node_allocated.values())

    def free_by_node(self, resource: str) -> dict[str, float]:
        return {
            node_id: cap.get(resource, 0.0) - self.node_allocated.get(node_id, {}).get(resource, 0.0)
            for node_id, cap in self.node_capacity.items()
        }

    def pending_task_requests(self) -> list[dict[str, float]]:
        return [
            self.task_request.get(task_id, {})
            for instance_id, status in self.instance_status.items()
            if status == "pending" and (task_id := self.instance_task.get(instance_id)) is not None
        ]

    def running_pod_counts_by_node(self) -> list[int]:
        counts = {node_id: 0 for node_id in self.node_capacity}
        for instance_id, status in self.instance_status.items():
            if status == "running":
                node_id = self.instance_node.get(instance_id)
                if node_id in counts:
                    counts[node_id] += 1
        return list(counts.values())


def _population_stdev(values: list[int]) -> float:
    if not values:
        return 0.0
    mean = sum(values) / len(values)
    variance = sum((v - mean) ** 2 for v in values) / len(values)
    return variance**0.5


def _is_fragmentation_blocked(world: _World) -> bool:
    """True if some pending task's request fits the cluster's aggregate free
    resources but fits no single node (experiment.md 3.4's 置けない空き資源の量)."""
    free_by_node_by_resource = {r: world.free_by_node(r) for r in world.resource_names()}
    for request in world.pending_task_requests():
        if not any(amount > 0 for amount in request.values()):
            continue
        fits_aggregate = all(
            amount <= 0 or sum(free_by_node_by_resource.get(r, {}).values()) >= amount
            for r, amount in request.items()
        )
        if not fits_aggregate:
            continue
        fits_some_node = any(
            all(amount <= 0 or free_by_node_by_resource.get(r, {}).get(node_id, 0.0) >= amount for r, amount in request.items())
            for node_id in world.node_capacity
        )
        if not fits_some_node:
            return True
    return False


def compute_metrics(transitions: list[Transition]) -> Metrics:
    """Computes one run's experiment 1 metrics from its transition record
    (units/U04.md Implementation Decisions, experiment.md 3.4)."""
    ordered = sorted(transitions, key=lambda t: (parse_time(t.time), t.seq))

    pending_wait_by_class: dict[str, list[float]] = {}
    nonzero_wait_count = 0
    wait_count_total = 0

    def on_wait(instance_id: str, task_id: str | None, wait: float) -> None:
        nonlocal nonzero_wait_count, wait_count_total
        request = world.task_request.get(task_id, {}) if task_id else {}
        cls = classify_gpu_class(request.get("nvidia.com/gpu", 0.0))
        pending_wait_by_class.setdefault(cls, []).append(wait)
        wait_count_total += 1
        if wait > 0:
            nonzero_wait_count += 1

    world = _World()
    if not ordered:
        return Metrics(
            pending_wait_by_class={},
            pending_count_at_end=0,
            allocation_rate={},
            unplaceable_free={},
            cluster_std=0.0,
            nonzero_wait_fraction=0.0,
        )

    alloc_weighted: dict[str, float] = {}
    alloc_duration: dict[str, float] = {}
    unplaceable_sum: dict[str, float] = {}
    cluster_std_weighted = 0.0
    cluster_std_duration = 0.0

    snapshots: list[tuple[float, _World]] = []
    i = 0
    n = len(ordered)
    while i < n:
        t_val = parse_time(ordered[i].time)
        j = i
        while j < n and ordered[j].time == ordered[i].time:
            world.apply(ordered[j], t_val, on_wait=on_wait)
            j += 1

        snapshots.append((t_val, copy.deepcopy(world)))
        i = j

    for k in range(len(snapshots) - 1):
        t_cur, snap = snapshots[k]
        t_next, _ = snapshots[k + 1]
        dur = t_next - t_cur
        if dur <= 0:
            continue

        for r in snap.resource_names():
            cap = snap.capacity_total(r)
            if cap > 0:
                alloc_weighted[r] = alloc_weighted.get(r, 0.0) + (snap.allocated_total(r) / cap) * dur
                alloc_duration[r] = alloc_duration.get(r, 0.0) + dur

        if _is_fragmentation_blocked(snap):
            for r in snap.resource_names():
                total_free = sum(snap.free_by_node(r).values())
                unplaceable_sum[r] = unplaceable_sum.get(r, 0.0) + total_free * dur

        counts = snap.running_pod_counts_by_node()
        if counts:
            cluster_std_weighted += _population_stdev(counts) * dur
            cluster_std_duration += dur

    allocation_rate = {r: alloc_weighted[r] / alloc_duration[r] for r in alloc_weighted if alloc_duration.get(r, 0.0) > 0}
    cluster_std = cluster_std_weighted / cluster_std_duration if cluster_std_duration > 0 else 0.0
    pending_count_at_end = sum(1 for status in world.instance_status.values() if status == "pending")
    nonzero_wait_fraction = nonzero_wait_count / wait_count_total if wait_count_total > 0 else 0.0

    return Metrics(
        pending_wait_by_class=pending_wait_by_class,
        pending_count_at_end=pending_count_at_end,
        allocation_rate=allocation_rate,
        unplaceable_free=unplaceable_sum,
        cluster_std=cluster_std,
        nonzero_wait_fraction=nonzero_wait_fraction,
    )


@dataclass(frozen=True)
class DiffResult:
    """A - B for one metric, across input versions (decisions.md).

    raw holds the single value when there is exactly one version; ci holds the
    bootstrap 95% confidence interval when there are more than one. Exactly one
    of the two is set.
    """

    raw: float | None
    ci: tuple[float, float] | None


def bootstrap_diff(diffs: list[float], rng, resamples: int = 10000) -> DiffResult:
    """Combines per-version diffs (scheduler A's metric minus scheduler B's, one
    value per input version) into the value to report: the diff itself when there
    is one version, otherwise a bootstrap 95% CI. rng is injected (decisions.md)
    so the same seed always yields the same CI.
    """
    if len(diffs) == 1:
        return DiffResult(raw=diffs[0], ci=None)
    diffs_arr = np.asarray(diffs, dtype=float)
    resampled_means = rng.choice(diffs_arr, size=(resamples, len(diffs_arr)), replace=True).mean(axis=1)
    low, high = np.percentile(resampled_means, [2.5, 97.5])
    return DiffResult(raw=None, ci=(float(low), float(high)))


@dataclass(frozen=True)
class VersionAverage:
    """One (input version, scheduler)'s metrics, averaged over its iterations
    (decisions.md: "反復の平均をその版の値とする"), ready to diff against another
    scheduler's VersionAverage for the same version."""

    pending_count_at_end: float
    allocation_rate: dict[str, float]
    unplaceable_free: dict[str, float]
    cluster_std: float
    nonzero_wait_fraction: float
    pending_wait_by_class: dict[str, list[float]]  # concatenated across iterations, for display
    mean_wait_by_class: dict[str, float]  # for diffing


def _mean(values: list[float]) -> float:
    return sum(values) / len(values)


def average_metrics(iterations: list[Metrics]) -> VersionAverage:
    """Averages one (version, scheduler)'s per-iteration Metrics into a VersionAverage."""
    resources = {r for m in iterations for r in set(m.allocation_rate) | set(m.unplaceable_free)}
    pending_wait_by_class: dict[str, list[float]] = {}
    for m in iterations:
        for cls, waits in m.pending_wait_by_class.items():
            pending_wait_by_class.setdefault(cls, []).extend(waits)

    return VersionAverage(
        pending_count_at_end=_mean([m.pending_count_at_end for m in iterations]),
        allocation_rate={r: _mean([m.allocation_rate.get(r, 0.0) for m in iterations]) for r in resources},
        unplaceable_free={r: _mean([m.unplaceable_free.get(r, 0.0) for m in iterations]) for r in resources},
        cluster_std=_mean([m.cluster_std for m in iterations]),
        nonzero_wait_fraction=_mean([m.nonzero_wait_fraction for m in iterations]),
        pending_wait_by_class=pending_wait_by_class,
        mean_wait_by_class={cls: _mean(waits) for cls, waits in pending_wait_by_class.items()},
    )


@dataclass(frozen=True)
class _VersionKey:
    trace_name: str
    trace_version: str
    input_seed: str


def _version_key(h: RunHeader) -> _VersionKey:
    return _VersionKey(h.trace_name, h.trace_version, h.input_seed)


@dataclass(frozen=True)
class RhoSection:
    """One ρ's worth of runs, grouped for display and diffing (units/U04.md)."""

    rho: float
    runs: list[tuple[RunRecord, Metrics]]
    # The two scheduler profiles found at this ρ, in diff order (a - b). LeastAllocated
    # sorts first when present, so experiment.md 3.1's prediction direction matches the
    # diffs' sign. None when this ρ does not have exactly two scheduler profiles.
    scheduler_order: tuple[str, str] | None
    diffs: dict[str, DiffResult]


def _scalar_pairs(a: VersionAverage, b: VersionAverage):
    yield "pending_count_at_end", a.pending_count_at_end, b.pending_count_at_end
    yield "cluster_std", a.cluster_std, b.cluster_std
    yield "nonzero_wait_fraction", a.nonzero_wait_fraction, b.nonzero_wait_fraction
    for r in sorted(set(a.allocation_rate) | set(b.allocation_rate)):
        yield f"allocation_rate:{r}", a.allocation_rate.get(r, 0.0), b.allocation_rate.get(r, 0.0)
    for r in sorted(set(a.unplaceable_free) | set(b.unplaceable_free)):
        yield f"unplaceable_free:{r}", a.unplaceable_free.get(r, 0.0), b.unplaceable_free.get(r, 0.0)
    for c in sorted(set(a.mean_wait_by_class) | set(b.mean_wait_by_class)):
        yield f"wait:{c}", a.mean_wait_by_class.get(c, 0.0), b.mean_wait_by_class.get(c, 0.0)


def _diffs_for_group(group: list[tuple[RunRecord, Metrics]], a: str, b: str, rng) -> dict[str, DiffResult]:
    by_version: dict[_VersionKey, dict[str, list[Metrics]]] = {}
    for record, metrics in group:
        vk = _version_key(record.header)
        by_version.setdefault(vk, {}).setdefault(record.header.scheduler_profile, []).append(metrics)

    per_metric_diffs: dict[str, list[float]] = {}
    for by_scheduler in by_version.values():
        if a not in by_scheduler or b not in by_scheduler:
            continue
        avg_a = average_metrics(by_scheduler[a])
        avg_b = average_metrics(by_scheduler[b])
        for key, va, vb in _scalar_pairs(avg_a, avg_b):
            per_metric_diffs.setdefault(key, []).append(va - vb)

    return {key: bootstrap_diff(diffs, rng=rng) for key, diffs in per_metric_diffs.items()}


def build_rho_sections(records: list[RunRecord], rng) -> list[RhoSection]:
    """Groups records by ρ and, within each ρ, computes the diff between its two
    scheduler profiles across input versions (decisions.md)."""
    runs = [(r, compute_metrics(r.transitions)) for r in records]
    by_rho: dict[float, list[tuple[RunRecord, Metrics]]] = {}
    for run in runs:
        by_rho.setdefault(run[0].header.rho, []).append(run)

    sections = []
    for rho in sorted(by_rho):
        group = by_rho[rho]
        schedulers = sorted({r.header.scheduler_profile for r, _ in group}, key=lambda s: (s != "LeastAllocated", s))
        scheduler_order = (schedulers[0], schedulers[1]) if len(schedulers) == 2 else None
        diffs = _diffs_for_group(group, *scheduler_order, rng) if scheduler_order else {}
        sections.append(RhoSection(rho=rho, runs=group, scheduler_order=scheduler_order, diffs=diffs))
    return sections


def _format_diff(diff: DiffResult) -> str:
    if diff.raw is not None:
        return f"{diff.raw:.4f}"
    return f"[{diff.ci[0]:.4f}, {diff.ci[1]:.4f}]"


def _prediction_holds(diff: DiffResult) -> bool:
    """True if the diff (LeastAllocated − the other scheduler) supports
    experiment.md 3.1's prediction that LeastAllocated's value is bigger. A
    bootstrap CI only counts as support when the whole interval is positive."""
    if diff.raw is not None:
        return diff.raw > 0
    return diff.ci[0] > 0


def render_rho_markdown(section: RhoSection) -> str:
    """Renders one ρ's section of the report (units/U04.md): a per-run metrics
    table, the diff between the two schedulers, and, for ρ = 0.5 and ρ = 1.1, the
    extra context scenarios 5 and 6 need to judge the negative control and the
    high-load prediction."""
    lines = [f"## ρ = {section.rho}", ""]

    lines.append("### 実行ごとのメトリクス")
    lines.append("")
    lines.append("| scheduler | iteration | pending_count_at_end | cluster_std | nonzero_wait_fraction |")
    lines.append("| --- | --- | --- | --- | --- |")
    for record, metrics in section.runs:
        lines.append(
            f"| {record.header.scheduler_profile} | {record.header.iteration} | "
            f"{metrics.pending_count_at_end} | {metrics.cluster_std:.4f} | {metrics.nonzero_wait_fraction:.4f} |"
        )
    lines.append("")

    if section.diffs:
        a, b = section.scheduler_order
        lines.append("### スケジューラの差")
        lines.append("")
        lines.append(f"{a} − {b}")
        lines.append("")
        lines.append("| metric | diff |")
        lines.append("| --- | --- |")
        for key in sorted(section.diffs):
            lines.append(f"| {key} | {_format_diff(section.diffs[key])} |")
        lines.append("")

    if section.rho == 0.5:
        lines.append("### 陰性対照")
        lines.append("")
        lines.append("待機時間が0でないPodの割合:")
        lines.append("")
        for record, metrics in section.runs:
            lines.append(f"- {record.header.scheduler_profile}: {metrics.nonzero_wait_fraction:.4f}")
        wait_keys = sorted(k for k in section.diffs if k.startswith("wait:"))
        if wait_keys:
            lines.append("")
            lines.append("待機時間の差:")
            lines.append("")
            for key in wait_keys:
                lines.append(f"- {key}: {_format_diff(section.diffs[key])}")
        lines.append("")

    if section.rho == 1.1 and section.scheduler_order and "LeastAllocated" in section.scheduler_order:
        lines.append("### 予測と結果（高負荷）")
        lines.append("")
        lines.append(
            "予測（experiment.md 3.1）: LeastAllocated の方が、大きな要求を持つタスクの"
            "待機時間と、置けない空き資源の量が大きい。"
        )
        lines.append("")
        prediction_keys = sorted(k for k in section.diffs if k.startswith("wait:") or k.startswith("unplaceable_free:"))
        if prediction_keys:
            lines.append("| metric | diff（LeastAllocated − 他） | 予測が当たったか |")
            lines.append("| --- | --- | --- |")
            for key in prediction_keys:
                diff = section.diffs[key]
                outcome = "当たった" if _prediction_holds(diff) else "当たらなかった"
                lines.append(f"| {key} | {_format_diff(diff)} | {outcome} |")
        lines.append("")

    return "\n".join(lines)


def build_figures(sections: list[RhoSection]) -> list[tuple[str, object]]:
    """Builds one boxplot figure per ρ, comparing the pending wait time
    distribution by GPU class and scheduler (scenario 4)."""
    import matplotlib

    matplotlib.use("Agg")
    import matplotlib.pyplot as plt

    figures = []
    for section in sections:
        fig, ax = plt.subplots()
        data: list[list[float]] = []
        labels: list[str] = []
        for record, metrics in section.runs:
            for cls in sorted(metrics.pending_wait_by_class):
                waits = metrics.pending_wait_by_class[cls]
                if waits:
                    data.append(waits)
                    labels.append(f"{record.header.scheduler_profile}\n{cls} GPU")
        if data:
            ax.boxplot(data, tick_labels=labels)
        ax.set_title(f"pending wait time (ρ={section.rho})")
        ax.set_ylabel("seconds")
        fig.tight_layout()
        figures.append((f"figures/rho-{section.rho}-wait.png", fig))
    return figures


class FileReportWriter:
    """The real report-writing adapter (decisions.md): writes to files under
    output_dir."""

    def __init__(self, output_dir: Path) -> None:
        self.output_dir = Path(output_dir)

    def write_text(self, relpath: str, content: str) -> None:
        path = self.output_dir / relpath
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content, encoding="utf-8")

    def write_figure(self, relpath: str, fig) -> None:
        path = self.output_dir / relpath
        path.parent.mkdir(parents=True, exist_ok=True)
        fig.savefig(path)
        import matplotlib.pyplot as plt

        plt.close(fig)


def write_report(records: list[RunRecord], writer, rng) -> None:
    """Builds and writes experiment 1's report (scenario 4): one section per ρ,
    with ρ = 0.5's negative control and ρ = 1.1's prediction/result pairing
    (scenarios 5 and 6)."""
    sections = build_rho_sections(records, rng)
    lines = ["# 実験1 集計報告", ""]
    for section in sections:
        lines.append(render_rho_markdown(section))
    writer.write_text("report.md", "\n".join(lines))
    for relpath, fig in build_figures(sections):
        writer.write_figure(relpath, fig)


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

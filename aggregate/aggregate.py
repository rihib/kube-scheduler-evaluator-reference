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
import math
import re
import sys
from collections.abc import Callable, Sequence
from dataclasses import dataclass
from datetime import UTC, datetime
from pathlib import Path
from typing import ClassVar

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
    # True when the file's last line was not valid JSON: the write was very likely
    # interrupted mid-line (units/U04.md "着手時に決めること"). That line is dropped;
    # callers should exclude a truncated record instead of treating its cut-off
    # point as a genuine end of observation.
    truncated: bool = False


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
    # None when the record has no interval with a registered node to measure
    # (round1 review, correctness IMO): that is "not measured", distinct from a
    # measured value of exactly 0.0 (e.g. every node had the same pod count).
    cluster_std: float | None
    # Fraction of instances with wait data whose pending dwell time was nonzero.
    # None when no instance ever had wait data (round1 review, correctness IMO),
    # distinct from a measured fraction of exactly 0.0.
    nonzero_wait_fraction: float | None


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
        # Dispatches on operation name (round1 review, maintainability IMO: the
        # previous single if/elif chain over both operation and state exceeded the
        # project's cyclomatic-complexity threshold). Each handler still checks
        # tr.state itself where one operation writes more than one mutable state.
        handler = self._HANDLERS.get(tr.operation)
        if handler is not None:
            handler(self, tr, time_val, on_wait)

    def _apply_register_node(self, tr: Transition, time_val: float, on_wait) -> None:
        if tr.state == StateResourceCapacity:
            self.node_capacity[tr.component_id] = _parsed_resources(tr.after)
            self.node_allocated.setdefault(tr.component_id, {})

    def _apply_deregister_node(self, tr: Transition, time_val: float, on_wait) -> None:
        self.node_capacity.pop(tr.component_id, None)
        self.node_allocated.pop(tr.component_id, None)

    def _apply_create_task_or_recreate(self, tr: Transition, time_val: float, on_wait) -> None:
        if tr.state != StateInstanceStatus:
            return
        self.instance_status[tr.component_id] = "pending"
        self.instance_task[tr.component_id] = tr.task_id
        self.pending_since[tr.component_id] = time_val
        if tr.task_immutable:
            self.task_request[tr.task_id] = _parsed_resources(tr.task_immutable.get("resource_request"))

    def _apply_assign_node(self, tr: Transition, time_val: float, on_wait) -> None:
        if tr.state == StateAssignedNode:
            self.instance_node[tr.component_id] = tr.after
        elif tr.state == StateAllocatedResource:
            node_id, instance_id = tr.component_id, tr.state_key
            alloc = _parsed_resources(tr.after)
            allocated = self.node_allocated.setdefault(node_id, {})
            for r, v in alloc.items():
                allocated[r] = allocated.get(r, 0.0) + v
            self.instance_allocation[instance_id] = (node_id, alloc)

    def _apply_start(self, tr: Transition, time_val: float, on_wait) -> None:
        instance_id = tr.component_id
        self.instance_status[instance_id] = "running"
        start = self.pending_since.pop(instance_id, None)
        if start is not None and on_wait is not None:
            on_wait(instance_id, self.instance_task.get(instance_id), time_val - start)

    def _apply_complete(self, tr: Transition, time_val: float, on_wait) -> None:
        self.instance_status[tr.component_id] = "completed"

    def _apply_delete(self, tr: Transition, time_val: float, on_wait) -> None:
        if tr.state == StateInstanceStatus:
            self.instance_status[tr.component_id] = "deleted"
        elif tr.state == StateAssignedNode:
            self.instance_node[tr.component_id] = None

    def _apply_release(self, tr: Transition, time_val: float, on_wait) -> None:
        instance_id = tr.state_key
        entry = self.instance_allocation.pop(instance_id, None)
        if entry is not None:
            node_id, alloc = entry
            allocated = self.node_allocated.setdefault(node_id, {})
            for r, v in alloc.items():
                allocated[r] = allocated.get(r, 0.0) - v

    _HANDLERS: ClassVar = {
        OpRegisterNode: _apply_register_node,
        OpDeregisterNode: _apply_deregister_node,
        OpCreateTask: _apply_create_task_or_recreate,
        OpRecreate: _apply_create_task_or_recreate,
        OpAssignNode: _apply_assign_node,
        OpStart: _apply_start,
        OpComplete: _apply_complete,
        OpDelete: _apply_delete,
        OpRelease: _apply_release,
    }

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


def _resource_free_aggregates(world: _World, resources: set[str]) -> tuple[dict[str, float], dict[str, float]]:
    """Per resource, the cluster-wide total free amount and the single largest
    per-node free amount — computed once per interval and reused for every
    pending task, instead of being rescanned for each one (round1 review,
    Critical: rescanning every node for every pending task made
    _is_fragmentation_blocked O(pending tasks x nodes x resources) per
    interval, which does not finish at openb's scale)."""
    free_sum: dict[str, float] = {}
    free_max: dict[str, float] = {}
    for r in resources:
        amounts = list(world.free_by_node(r).values())
        free_sum[r] = sum(amounts)
        free_max[r] = max(amounts, default=0.0)
    return free_sum, free_max


def _is_fragmentation_blocked(world: _World, free_sum: dict[str, float], free_max: dict[str, float]) -> bool:
    """True if some pending task's request fits the cluster's aggregate free
    resources but fits no single node (experiment.md 3.4's 置けない空き資源の量).

    Checks each required resource's own max independently rather than scanning
    every node to find one that satisfies all of a task's resources at once
    (round1 review, Critical — see _resource_free_aggregates). This can miss a
    real fragmentation case where every resource individually has *some* node
    with enough of it, but no *single* node has enough of all of them
    together; it never reports fragmentation that is not real, because a
    resource whose own max is too small rules out every node on that resource
    alone.
    """
    for request in world.pending_task_requests():
        if not any(amount > 0 for amount in request.values()):
            continue
        fits_aggregate = all(amount <= 0 or free_sum.get(r, 0.0) >= amount for r, amount in request.items())
        if not fits_aggregate:
            continue
        fits_some_node = all(amount <= 0 or free_max.get(r, 0.0) >= amount for r, amount in request.items())
        if not fits_some_node:
            return True
    return False


class _MetricsAccumulator:
    """Integrates compute_metrics' three time-weighted metrics (allocation
    rate, unplaceable free resources, Cluster_std) one interval at a time, so
    the caller never has to keep every past world state alive at once (round1
    review, Critical: a full-history list of deep-copied worlds used tens of
    GB at openb's scale)."""

    def __init__(self) -> None:
        self._alloc_weighted: dict[str, float] = {}
        self._alloc_duration: dict[str, float] = {}
        self.unplaceable_free: dict[str, float] = {}
        self._cluster_std_weighted = 0.0
        self._cluster_std_duration = 0.0

    def add_interval(self, world: _World, duration: float) -> None:
        if duration <= 0:
            return
        resources = world.resource_names()
        free_sum, free_max = _resource_free_aggregates(world, resources)

        for r in resources:
            cap = world.capacity_total(r)
            if cap > 0:
                self._alloc_weighted[r] = self._alloc_weighted.get(r, 0.0) + (world.allocated_total(r) / cap) * duration
                self._alloc_duration[r] = self._alloc_duration.get(r, 0.0) + duration

        if _is_fragmentation_blocked(world, free_sum, free_max):
            for r in resources:
                self.unplaceable_free[r] = self.unplaceable_free.get(r, 0.0) + free_sum[r] * duration

        counts = world.running_pod_counts_by_node()
        if counts:
            self._cluster_std_weighted += _population_stdev(counts) * duration
            self._cluster_std_duration += duration

    def allocation_rate(self) -> dict[str, float]:
        return {r: self._alloc_weighted[r] / self._alloc_duration[r] for r in self._alloc_weighted if self._alloc_duration.get(r, 0.0) > 0}

    def cluster_std(self) -> float | None:
        if self._cluster_std_duration <= 0:
            return None
        return self._cluster_std_weighted / self._cluster_std_duration


def _censor_still_pending_waits(
    world: _World, observation_end: float, pending_wait_by_class: dict[str, list[float]]
) -> tuple[int, int]:
    """Adds one right-censored wait (observation_end - pending_since) for every
    instance still pending when the record ends, instead of leaving them out
    of both the distribution and nonzero_wait_fraction's denominator (round1
    review, Must: excluding them made a scheduler that leaves more tasks
    stuck pending look like it has a *smaller* nonzero-wait fraction and a
    *shorter* wait distribution — the opposite of what actually happened).
    Returns (count added, count with a nonzero wait) for the caller to fold
    into its own totals.
    """
    added = 0
    nonzero = 0
    for instance_id, start in world.pending_since.items():
        if world.instance_status.get(instance_id) != "pending":
            continue  # deleted while still unassigned: not a censored wait
        task_id = world.instance_task.get(instance_id)
        request = world.task_request.get(task_id, {}) if task_id else {}
        cls = classify_gpu_class(request.get("nvidia.com/gpu", 0.0))
        wait = max(0.0, observation_end - start)
        pending_wait_by_class.setdefault(cls, []).append(wait)
        added += 1
        if wait > 0:
            nonzero += 1
    return added, nonzero


def compute_metrics(transitions: Sequence[Transition]) -> Metrics:
    """Computes one run's experiment 1 metrics from its transition record
    (units/U04.md Implementation Decisions, experiment.md 3.4)."""
    ordered = sorted(transitions, key=lambda t: (parse_time(t.time), t.seq))
    if not ordered:
        return Metrics(
            pending_wait_by_class={},
            pending_count_at_end=0,
            allocation_rate={},
            unplaceable_free={},
            cluster_std=None,
            nonzero_wait_fraction=None,
        )

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
    accumulator = _MetricsAccumulator()

    i = 0
    n = len(ordered)
    prev_time: float | None = None
    while i < n:
        t_val = parse_time(ordered[i].time)
        if prev_time is not None:
            accumulator.add_interval(world, t_val - prev_time)
        j = i
        while j < n and ordered[j].time == ordered[i].time:
            world.apply(ordered[j], t_val, on_wait=on_wait)
            j += 1
        prev_time = t_val
        i = j

    observation_end = prev_time if prev_time is not None else 0.0
    censored_added, censored_nonzero = _censor_still_pending_waits(world, observation_end, pending_wait_by_class)
    wait_count_total += censored_added
    nonzero_wait_count += censored_nonzero

    pending_count_at_end = sum(1 for status in world.instance_status.values() if status == "pending")
    nonzero_wait_fraction = nonzero_wait_count / wait_count_total if wait_count_total > 0 else None

    return Metrics(
        pending_wait_by_class=pending_wait_by_class,
        pending_count_at_end=pending_count_at_end,
        allocation_rate=accumulator.allocation_rate(),
        unplaceable_free=accumulator.unplaceable_free,
        cluster_std=accumulator.cluster_std(),
        nonzero_wait_fraction=nonzero_wait_fraction,
    )


@dataclass(frozen=True)
class DiffResult:
    """A - B for one metric, across input versions (decisions.md).

    raw holds the single value when there is exactly one version; ci holds the
    bootstrap 95% confidence interval when there are more than one. Exactly one
    of the two is set — enforced below (round1 review, maintainability IMO),
    so a DiffResult that violates it fails at construction instead of later,
    confusingly, inside whichever of _format_diff / _prediction_holds happens
    to read the unset field first.
    """

    raw: float | None
    ci: tuple[float, float] | None

    def __post_init__(self) -> None:
        if (self.raw is None) == (self.ci is None):
            raise ValueError("DiffResult requires exactly one of raw or ci to be set")


def bootstrap_diff(per_version_diffs: Sequence[float], *, seed: int, iterations: int = 10000) -> DiffResult:
    """Combines per-version diffs (scheduler A's metric minus scheduler B's, one
    value per input version) into the value to report: the diff itself when there
    is one version, otherwise a bootstrap 95% CI.

    Takes a plain seed rather than an injected random number generator: a fresh
    generator is built from it on every call, so this metric's CI depends only
    on (per_version_diffs, seed) and not on how many other metrics were
    bootstrapped first against a shared generator. That keeps this seam stable
    across decisions.md's later step of scaling up the number of input versions
    and iterations (rihib:interface-judge round 1, Must 1): adding a version
    only changes the length of per_version_diffs, not this function's inputs'
    shape, and no other metric's result shifts because of it.
    """
    if len(per_version_diffs) == 1:
        return DiffResult(raw=per_version_diffs[0], ci=None)
    rng = np.random.default_rng(seed)
    diffs_arr = np.asarray(per_version_diffs, dtype=float)
    resampled_means = rng.choice(diffs_arr, size=(iterations, len(diffs_arr)), replace=True).mean(axis=1)
    low, high = np.percentile(resampled_means, [2.5, 97.5])
    return DiffResult(raw=None, ci=(float(low), float(high)))


@dataclass(frozen=True)
class VersionAverage:
    """One (input version, scheduler)'s metrics, averaged over its iterations.

    U04.md's Implementation Decisions ("差の求め方") decide to bootstrap the
    diff across input *versions*, but leave open how to fold one version's
    *iterations* into the single value that diff is computed from. Averaging
    them is this implementation's own reading of that decision, not a
    quotation from decisions.md or U04.md (round1 review, maintainability
    Must: an earlier version of this docstring quoted "反復の平均をその版の値と
    する" as if it were spec text, but no such sentence exists in either
    document).
    """

    pending_count_at_end: float
    allocation_rate: dict[str, float]
    unplaceable_free: dict[str, float]
    cluster_std: float | None
    nonzero_wait_fraction: float | None
    pending_wait_by_class: dict[str, list[float]]  # concatenated across iterations, for display
    mean_wait_by_class: dict[str, float]  # for diffing


def _mean(values: list[float]) -> float:
    return sum(values) / len(values)


def _mean_optional(values: list[float | None]) -> float | None:
    """Averages the non-None values, skipping iterations with no data for this
    metric (Metrics.cluster_std / nonzero_wait_fraction can be None). None if
    every iteration had no data."""
    present = [v for v in values if v is not None]
    return _mean(present) if present else None


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
        cluster_std=_mean_optional([m.cluster_std for m in iterations]),
        nonzero_wait_fraction=_mean_optional([m.nonzero_wait_fraction for m in iterations]),
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
    # Every distinct scheduler profile found at this ρ, sorted. Kept even when
    # there are not exactly two, so the report can say which profiles it found
    # instead of silently dropping the diff section (round1 review, Must).
    found_schedulers: tuple[str, ...]
    # The two scheduler profiles found at this ρ, in diff order (a - b). None
    # when found_schedulers does not have exactly two entries.
    scheduler_order: tuple[str, str] | None
    diffs: dict[str, DiffResult]
    # The scheduler experiment.md 3.1 predicts will have the bigger value
    # (passed in by the caller, round1 review, Must: the previous version
    # matched the literal string "LeastAllocated", so a differently-named
    # distributed-type profile would silently lose its prediction section).
    distributed_scheduler: str


def _scalar_pairs(a: VersionAverage, b: VersionAverage):
    yield "pending_count_at_end", a.pending_count_at_end, b.pending_count_at_end
    if a.cluster_std is not None and b.cluster_std is not None:
        yield "cluster_std", a.cluster_std, b.cluster_std
    if a.nonzero_wait_fraction is not None and b.nonzero_wait_fraction is not None:
        yield "nonzero_wait_fraction", a.nonzero_wait_fraction, b.nonzero_wait_fraction
    for r in sorted(set(a.allocation_rate) | set(b.allocation_rate)):
        yield f"allocation_rate:{r}", a.allocation_rate.get(r, 0.0), b.allocation_rate.get(r, 0.0)
    for r in sorted(set(a.unplaceable_free) | set(b.unplaceable_free)):
        yield f"unplaceable_free:{r}", a.unplaceable_free.get(r, 0.0), b.unplaceable_free.get(r, 0.0)
    for c in sorted(set(a.mean_wait_by_class) | set(b.mean_wait_by_class)):
        yield f"wait:{c}", a.mean_wait_by_class.get(c, 0.0), b.mean_wait_by_class.get(c, 0.0)


def _diffs_for_group(group: list[tuple[RunRecord, Metrics]], a: str, b: str, seed: int) -> dict[str, DiffResult]:
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

    return {key: bootstrap_diff(diffs, seed=seed) for key, diffs in per_metric_diffs.items()}


def build_rho_sections(records: Sequence[RunRecord], *, seed: int, distributed_scheduler: str = "LeastAllocated") -> list[RhoSection]:
    """Groups records by ρ and, within each ρ, computes the diff between its two
    scheduler profiles across input versions (decisions.md).

    distributed_scheduler names the scheduler experiment.md 3.1 predicts will
    have the bigger value, so render_rho_markdown's ρ = 1.1 section can find it
    by the caller's own say-so rather than a hardcoded profile name.
    """
    runs = [(r, compute_metrics(r.transitions)) for r in records]
    by_rho: dict[float, list[tuple[RunRecord, Metrics]]] = {}
    for run in runs:
        by_rho.setdefault(run[0].header.rho, []).append(run)

    sections = []
    for rho in sorted(by_rho):
        group = by_rho[rho]
        found_schedulers = tuple(sorted({r.header.scheduler_profile for r, _ in group}, key=lambda s: (s != distributed_scheduler, s)))
        scheduler_order = (found_schedulers[0], found_schedulers[1]) if len(found_schedulers) == 2 else None
        diffs = _diffs_for_group(group, *scheduler_order, seed) if scheduler_order else {}
        sections.append(
            RhoSection(
                rho=rho,
                runs=group,
                found_schedulers=found_schedulers,
                scheduler_order=scheduler_order,
                diffs=diffs,
                distributed_scheduler=distributed_scheduler,
            )
        )
    return sections


RHO_NEGATIVE_CONTROL = 0.5  # experiment.md 2.3: ρ = 0.5 is the negative control.
RHO_HIGH_LOAD = 1.1  # experiment.md 2.3: ρ = 1.1 is the highest load level.
_RHO_TOLERANCE = 1e-6  # Round1 review, Must: == on floats can miss a ρ meant to match.


def _rho_is(rho: float, target: float) -> bool:
    return math.isclose(rho, target, abs_tol=_RHO_TOLERANCE)


def _figure_relpath(rho: float) -> str:
    return f"figures/rho-{rho}-wait.png"


def _format_diff(diff: DiffResult) -> str:
    if diff.raw is not None:
        return f"{diff.raw:.4f}"
    return f"[{diff.ci[0]:.4f}, {diff.ci[1]:.4f}]"


def _format_optional(value: float | None, digits: int = 4) -> str:
    return "n/a" if value is None else f"{value:.{digits}f}"


def _prediction_holds(diff: DiffResult) -> bool:
    """True if the diff (the distributed scheduler − the other) supports
    experiment.md 3.1's prediction that the distributed scheduler's value is
    bigger. A bootstrap CI only counts as support when the whole interval is
    positive."""
    if diff.raw is not None:
        return diff.raw > 0
    return diff.ci[0] > 0


def _is_large_request_metric(key: str) -> bool:
    """True if key is about the "large要求を持つタスク" experiment.md 3.1's
    high-load prediction actually makes a claim about: unplaceable_free
    (not scoped to a GPU class) and wait times for the 2-4 and 8 GPU classes.
    The prediction says nothing about the 0 and 1 GPU classes (round1 review,
    Must: the previous version gave every class a 当たった/当たらなかった verdict,
    which misrepresented the small classes as part of the prediction)."""
    if key.startswith("unplaceable_free:"):
        return True
    if key.startswith("wait:"):
        return key.split(":", 1)[1] in ("2-4", "8")
    return False


def render_rho_markdown(section: RhoSection) -> str:
    """Renders one ρ's section of the report (units/U04.md): a per-run metrics
    table, the diff between the two schedulers, and, for ρ = 0.5 and ρ = 1.1, the
    extra context scenarios 5 and 6 need to judge the negative control and the
    high-load prediction."""
    resources = sorted({r for _, m in section.runs for r in set(m.allocation_rate) | set(m.unplaceable_free)})
    lines = [f"## ρ = {section.rho}", ""]

    lines.append("### 実行ごとのメトリクス")
    lines.append("")
    header_cols = ["scheduler", "iteration", "pending_count_at_end", "cluster_std", "nonzero_wait_fraction"]
    header_cols += [f"allocation_rate:{r}" for r in resources] + [f"unplaceable_free:{r}" for r in resources]
    lines.append("| " + " | ".join(header_cols) + " |")
    lines.append("| " + " | ".join("---" for _ in header_cols) + " |")
    for record, metrics in section.runs:
        row = [
            record.header.scheduler_profile,
            str(record.header.iteration),
            str(metrics.pending_count_at_end),
            _format_optional(metrics.cluster_std),
            _format_optional(metrics.nonzero_wait_fraction),
        ]
        row += [f"{metrics.allocation_rate.get(r, 0.0):.4f}" for r in resources]
        row += [f"{metrics.unplaceable_free.get(r, 0.0):.4f}" for r in resources]
        lines.append("| " + " | ".join(row) + " |")
    lines.append("")

    lines.append("### 待機時間の分布")
    lines.append("")
    lines.append(f"![pending wait time distribution for ρ = {section.rho}]({_figure_relpath(section.rho)})")
    lines.append("")

    if section.scheduler_order:
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
    else:
        lines.append("### スケジューラの差")
        lines.append("")
        lines.append(
            f"この ρ では {len(section.found_schedulers)} 個のスケジューラのプロファイルが"
            f"見つかった: {', '.join(section.found_schedulers) or '(なし)'}。"
            "差は、プロファイルがちょうど2つのときだけ計算する。"
        )
        lines.append("")

    if _rho_is(section.rho, RHO_NEGATIVE_CONTROL):
        lines.append("### 陰性対照")
        lines.append("")
        lines.append("待機時間が0でないPodの割合:")
        lines.append("")
        for record, metrics in section.runs:
            lines.append(f"- {record.header.scheduler_profile}: {_format_optional(metrics.nonzero_wait_fraction)}")
        wait_keys = sorted(k for k in section.diffs if k.startswith("wait:"))
        if wait_keys:
            lines.append("")
            lines.append("待機時間の差:")
            lines.append("")
            for key in wait_keys:
                lines.append(f"- {key}: {_format_diff(section.diffs[key])}")
        lines.append("")

    if _rho_is(section.rho, RHO_HIGH_LOAD):
        lines.append("### 予測と結果（高負荷）")
        lines.append("")
        lines.append(
            f"予測（experiment.md 3.1）: {section.distributed_scheduler} の方が、大きな要求を持つ"
            "タスクの待機時間と、置けない空き資源の量が大きい。"
        )
        lines.append("")
        if not section.scheduler_order or section.distributed_scheduler not in section.scheduler_order:
            lines.append(
                f"この ρ の記録に分散型として指定されたスケジューラ（{section.distributed_scheduler}）が"
                f"見つからない（見つかったプロファイル: {', '.join(section.found_schedulers) or '(なし)'}）。"
                "予測と結果は示せない。"
            )
            lines.append("")
        else:
            all_keys = sorted(k for k in section.diffs if k.startswith(("wait:", "unplaceable_free:")))
            if all_keys:
                diff_label = f"diff（{section.distributed_scheduler} − 他）"
                lines.append(f"| metric | {diff_label} | 予測が当たったか |")
                lines.append("| --- | --- | --- |")
                for key in all_keys:
                    diff = section.diffs[key]
                    if _is_large_request_metric(key):
                        outcome = "当たった" if _prediction_holds(diff) else "当たらなかった"
                    else:
                        outcome = "—（予測の対象外）"
                    lines.append(f"| {key} | {_format_diff(diff)} | {outcome} |")
            lines.append("")

    return "\n".join(lines)


def build_figures(sections: list[RhoSection]) -> list[tuple[str, bytes]]:
    """Builds one boxplot PNG per ρ, comparing the pending wait time
    distribution by GPU class and scheduler (scenario 4). Rendering happens
    here, in production code, and only the finished PNG bytes cross the writer
    seam (rihib:interface-judge round 1, IMO): a test can check the bytes it
    received actually are a PNG, instead of only checking that some Figure
    object was passed through. render_rho_markdown links to the same path
    (_figure_relpath) so the report's text actually shows the figure
    (round1 review, Must), instead of only writing a PNG file nothing refers to."""
    import io

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

        buf = io.BytesIO()
        fig.savefig(buf, format="png")
        plt.close(fig)
        figures.append((_figure_relpath(section.rho), buf.getvalue()))
    return figures


class FileReportWriter:
    """The real report-writing adapter (decisions.md): writes to files under
    output_dir. A thin port (write_text / write_bytes only): it has no idea
    what a figure is, so it cannot be satisfied by just recording that
    write_figure(fig) was called with something."""

    def __init__(self, output_dir: Path) -> None:
        self.output_dir = Path(output_dir)

    def write_text(self, relpath: str, content: str) -> None:
        path = self.output_dir / relpath
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content, encoding="utf-8")

    def write_bytes(self, relpath: str, content: bytes) -> None:
        path = self.output_dir / relpath
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(content)


def write_report(records: Sequence[RunRecord], writer, *, seed: int, distributed_scheduler: str = "LeastAllocated") -> None:
    """Builds and writes experiment 1's report (scenario 4): one section per ρ,
    with ρ = 0.5's negative control and ρ = 1.1's prediction/result pairing
    (scenarios 5 and 6)."""
    sections = build_rho_sections(records, seed=seed, distributed_scheduler=distributed_scheduler)
    lines = ["# 実験1 集計報告", ""]
    for section in sections:
        lines.append(render_rho_markdown(section))
    writer.write_text("report.md", "\n".join(lines))
    for relpath, png_bytes in build_figures(sections):
        writer.write_bytes(relpath, png_bytes)


def _parse_args(argv: list[str]):
    import argparse

    parser = argparse.ArgumentParser(description="Aggregate experiment 1 transition records into a report.")
    parser.add_argument("--records-dir", required=True, help="Folder of *.jsonl transition records.")
    parser.add_argument("--output-dir", required=True, help="Folder to write report.md and figures/ into.")
    parser.add_argument("--seed", type=int, default=0, help="Seed for the bootstrap CI's random number generator.")
    parser.add_argument(
        "--distributed-scheduler",
        default="LeastAllocated",
        help="The scheduler profile name experiment.md 3.1 predicts will have the bigger value at ρ = 1.1.",
    )
    return parser.parse_args(argv)


def run(
    argv: list[str],
    *,
    read_record_texts: Callable[[Path], Sequence[tuple[Path, str]]] | None = None,
    writer_factory: Callable[[Path], object] = FileReportWriter,
) -> int:
    """The report command (units/U04.md): reads every record under --records-dir
    and writes the report under --output-dir.

    read_record_texts and writer_factory are injected (decisions.md) so tests
    can pass Fakes; the defaults are the real file-based adapters.
    read_record_texts is deliberately thin — it only returns each file's path
    and raw text — because parsing (parse_record) is this module's own logic
    and always runs for real, never behind a Fake (rihib:interface-judge
    round 1, Must 2). Returning the path alongside the text (not just the
    text) lets a parse failure name which file it came from (round1 review,
    IMO).
    """
    if read_record_texts is None:
        read_record_texts = read_record_texts_from_folder
    args = _parse_args(argv)

    try:
        texts = read_record_texts(Path(args.records_dir))
    except Exception as e:
        print(f"error: failed to read records from {args.records_dir}: {e}", file=sys.stderr)
        return 1
    if not texts:
        print(f"error: no records found in {args.records_dir}", file=sys.stderr)
        return 1

    parsed = []
    for path, text in texts:
        try:
            parsed.append(parse_record(text))
        except Exception as e:
            print(f"error: failed to parse {path}: {e}", file=sys.stderr)
            return 1

    records = [r for r in parsed if not r.truncated]
    truncated_count = len(parsed) - len(records)
    if truncated_count:
        print(f"warning: skipped {truncated_count} truncated record(s) under {args.records_dir}", file=sys.stderr)
    if not records:
        print(f"error: no complete records to aggregate under {args.records_dir}", file=sys.stderr)
        return 1

    writer = writer_factory(Path(args.output_dir))
    try:
        write_report(records, writer, seed=args.seed, distributed_scheduler=args.distributed_scheduler)
    except OSError as e:
        # Only a failure to write the files themselves is reported as "the report
        # command failed" (round1 review, Must): a bug inside metrics computation
        # or matplotlib (TypeError, ZeroDivisionError, ...) propagates and crashes
        # instead of being relabeled as an output-directory problem with no
        # traceback.
        print(f"error: failed to write report to {args.output_dir}: {e}", file=sys.stderr)
        return 1
    return 0


def main() -> None:
    sys.exit(run(sys.argv[1:]))


def parse_record(text: str) -> RunRecord:
    """Parses one run's raw JSONL record text (header line + transition lines)
    into a RunRecord. This is the pure parsing step (module 4's side, per
    rihib:interface-judge's review): it always runs for real, never behind a
    Fake, so every parsing edge case — missing fields, an unknown operation
    name, a cut-off last line — gets real unit test coverage.

    If the last non-empty line is not valid JSON, the write was very likely
    interrupted mid-line (units/U04.md "着手時に決めること": a short-but-complete
    run and a run cut off mid-write look identical once the cut lands exactly on
    a line boundary, but a cut mid-line is detectable). That line is dropped and
    the record comes back with truncated=True. Any other malformed line is a
    hard error: it is not explained by a mid-write cut, so it is a genuine
    parsing failure the caller should not silently paper over.
    """
    raw_lines = [line for line in text.splitlines() if line.strip()]
    if not raw_lines:
        raise ValueError("record is empty, missing its header line")

    truncated = False
    try:
        json.loads(raw_lines[-1])
    except json.JSONDecodeError:
        truncated = True
        raw_lines = raw_lines[:-1]

    if not raw_lines:
        raise ValueError("record is empty after dropping its truncated last line")

    lines = [json.loads(line) for line in raw_lines]

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
    return RunRecord(header=header, transitions=transitions, truncated=truncated)


def read_record_texts_from_folder(folder: Path) -> list[tuple[Path, str]]:
    """The real adapter for run()'s --records-dir: returns each *.jsonl file's
    path and raw text, unparsed. Parsing (parse_record) always runs for real
    afterwards, so this seam is only ever asked to supply bytes, never to
    judge whether a record is complete. The path travels with the text so a
    parse failure can say which file it came from (round1 review, IMO)."""
    return [(p, p.read_text(encoding="utf-8")) for p in sorted(Path(folder).glob("*.jsonl"))]


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
    dt = datetime.strptime(m.group("date"), "%Y-%m-%dT%H:%M:%S").replace(tzinfo=UTC)
    seconds = dt.timestamp()
    if m.group("frac"):
        seconds += float(f"0.{m.group('frac')}")
    tz = m.group("tz")
    if tz != "Z":
        sign = 1 if tz[0] == "+" else -1
        hh, mm = tz[1:].split(":")
        seconds -= sign * (int(hh) * 3600 + int(mm) * 60)
    return seconds


if __name__ == "__main__":
    main()

#!/usr/bin/env bash
set -euo pipefail

python3 - "${VICTORIAMETRICS_URL:-http://localhost:8428}" <<'PY'
import json
import sys
import time
import urllib.parse
import urllib.request

vm_url = sys.argv[1]
query_time = int(time.time() + 24 * 60 * 60)

def query_result(expression):
    params = urllib.parse.urlencode({"query": expression, "time": query_time})
    with urllib.request.urlopen(f"{vm_url}/api/v1/query?{params}") as response:
        payload = json.load(response)
    return payload.get("data", {}).get("result", [])

def query_value(expression):
    result = query_result(expression)
    if len(result) != 1:
        return None
    return float(result[0]["value"][1])

latest = query_result(
    'topk(1, max by (evaluation_id) '
    '(max_over_time(virtualtime_created_at_miliseconds{obj_kind="scenario"}[2d])))'
)
if len(latest) != 1:
    raise SystemExit("could not identify the latest evaluation")
evaluation_id = latest[0].get("metric", {}).get("evaluation_id")
if not evaluation_id:
    raise SystemExit(f"latest evaluation has no evaluation_id: {latest}")

selector = f'evaluation_id="{evaluation_id}"'
queries = {
    "baseline_peak": f'max(max_over_time(gpu_allocation_percent{{{selector},scenario_id="scenario-gpu-utilization-binpack"}}[2d]))',
    "best_fit_peak": f'max(max_over_time(gpu_allocation_percent{{{selector},scenario_id="scenario-gpu-best-fit-binpack"}}[2d]))',
    "baseline_average": f'max(max_over_time(gpu_average_allocation_percent{{{selector},scenario_id="scenario-gpu-utilization-binpack"}}[2d]))',
    "best_fit_average": f'max(max_over_time(gpu_average_allocation_percent{{{selector},scenario_id="scenario-gpu-best-fit-binpack"}}[2d]))',
    "baseline_minutes": f'(max(max_over_time(virtualtime_deleted_at_miliseconds{{obj_kind="pod",{selector},scenario_id="scenario-gpu-utilization-binpack"}}[2d])) - min(max_over_time(virtualtime_created_at_miliseconds{{obj_kind="pod",{selector},scenario_id="scenario-gpu-utilization-binpack"}}[2d]))) / 60000',
    "best_fit_minutes": f'(max(max_over_time(virtualtime_deleted_at_miliseconds{{obj_kind="pod",{selector},scenario_id="scenario-gpu-best-fit-binpack"}}[2d])) - min(max_over_time(virtualtime_created_at_miliseconds{{obj_kind="pod",{selector},scenario_id="scenario-gpu-best-fit-binpack"}}[2d]))) / 60000',
}

for _ in range(20):
    values = {name: query_value(expression) for name, expression in queries.items()}
    if all(value is not None for value in values.values()):
        break
    time.sleep(0.5)
else:
    if values.get("baseline_minutes") is not None and values.get("best_fit_minutes") is not None:
        raise SystemExit(
            "GPU metrics are missing although Pod metrics exist. "
            "The binary was built without the evaluator PR's GPU metric support. "
            "Switch ../kube-scheduler-evaluator to "
            "agent/kubecon-gpu-binpacking-demo, then rerun `make demo`. "
            f"Values: {values}"
        )
    raise SystemExit(f"demo metrics did not become queryable: {values}")

for name in ("baseline_peak", "best_fit_peak", "baseline_average", "best_fit_average"):
    if not 0 <= values[name] <= 100:
        raise SystemExit(f"invalid GPU percentage: {values}")
for name in ("baseline_minutes", "best_fit_minutes"):
    if values[name] <= 0:
        raise SystemExit(f"invalid scenario completion time: {values}")
print(
    f"verified GPU allocation metrics for {evaluation_id}: "
    f"peak={values['baseline_peak']:.2f}%→{values['best_fit_peak']:.2f}%, "
    f"average={values['baseline_average']:.2f}%→{values['best_fit_average']:.2f}%, "
    f"completion={values['baseline_minutes']:.1f}m→{values['best_fit_minutes']:.1f}m"
)
PY

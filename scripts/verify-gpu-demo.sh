#!/usr/bin/env bash
set -euo pipefail

python3 - "${VICTORIAMETRICS_URL:-http://localhost:8428}" <<'PY'
import json
import sys
import time
import urllib.parse
import urllib.request

vm_url = sys.argv[1]
query_time = int(time.time() + 200 * 24 * 60 * 60)
queries = {
    "baseline_peak": 'max(max_over_time(gpu_allocation_percent{scenario_id="scenario-gpu-utilization-binpack"}[220d]))',
    "best_fit_peak": 'max(max_over_time(gpu_allocation_percent{scenario_id="scenario-gpu-best-fit-binpack"}[220d]))',
    "baseline_average": 'max(max_over_time(gpu_average_allocation_percent{scenario_id="scenario-gpu-utilization-binpack"}[220d]))',
    "best_fit_average": 'max(max_over_time(gpu_average_allocation_percent{scenario_id="scenario-gpu-best-fit-binpack"}[220d]))',
    "baseline_minutes": '(max(max_over_time(virtualtime_deleted_at_miliseconds{obj_kind="pod",scenario_id="scenario-gpu-utilization-binpack"}[220d])) - min(max_over_time(virtualtime_created_at_miliseconds{obj_kind="pod",scenario_id="scenario-gpu-utilization-binpack"}[220d]))) / 60000',
    "best_fit_minutes": '(max(max_over_time(virtualtime_deleted_at_miliseconds{obj_kind="pod",scenario_id="scenario-gpu-best-fit-binpack"}[220d])) - min(max_over_time(virtualtime_created_at_miliseconds{obj_kind="pod",scenario_id="scenario-gpu-best-fit-binpack"}[220d]))) / 60000',
}

def query(expression):
    params = urllib.parse.urlencode({"query": expression, "time": query_time})
    with urllib.request.urlopen(f"{vm_url}/api/v1/query?{params}") as response:
        payload = json.load(response)
    result = payload.get("data", {}).get("result", [])
    if len(result) != 1:
        return None
    return float(result[0]["value"][1])

for _ in range(20):
    values = {name: query(expression) for name, expression in queries.items()}
    if all(value is not None for value in values.values()):
        break
    time.sleep(0.5)
else:
    if values.get("baseline_minutes") is not None and values.get("best_fit_minutes") is not None:
        raise SystemExit(
            "GPU metrics are missing although Pod metrics exist. "
            "Run `make build-demo` to rebuild against "
            "../kube-scheduler-evaluator and confirm both repositories are on "
            "agent/kubecon-gpu-binpacking-demo. "
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
    "verified GPU allocation metrics: "
    f"peak={values['baseline_peak']:.2f}%→{values['best_fit_peak']:.2f}%, "
    f"average={values['baseline_average']:.2f}%→{values['best_fit_average']:.2f}%, "
    f"completion={values['baseline_minutes']:.1f}m→{values['best_fit_minutes']:.1f}m"
)
PY

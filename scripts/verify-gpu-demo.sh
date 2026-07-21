#!/usr/bin/env bash
set -euo pipefail

python3 - "${VICTORIAMETRICS_URL:-http://localhost:8428}" <<'PY'
import json
import sys
import time
import urllib.parse
import urllib.request

vm_url = sys.argv[1]
query_time = int(time.time() + 3 * 60 * 60)
queries = {
    "baseline": 'max(max_over_time(gpu_allocation_percent{scope="cluster",scenario_id="scenario-gpu-utilization-binpack"}[4h]))',
    "best_fit": 'max(max_over_time(gpu_allocation_percent{scope="cluster",scenario_id="scenario-gpu-best-fit-binpack"}[4h]))',
    "baseline_small": 'max(max_over_time(gpu_allocated_gpus{scope="node",node="gpu-small",scenario_id="scenario-gpu-utilization-binpack"}[4h]))',
    "best_fit_small": 'max(max_over_time(gpu_allocated_gpus{scope="node",node="gpu-small",scenario_id="scenario-gpu-best-fit-binpack"}[4h]))',
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
    raise SystemExit(f"GPU metrics did not become queryable: {values}")

baseline = values["baseline"]
best_fit = values["best_fit"]
if baseline >= best_fit:
    raise SystemExit(f"expected best-fit to improve peak allocation: baseline={baseline}, best-fit={best_fit}")
if best_fit != 100:
    raise SystemExit(f"expected best-fit peak allocation to be 100%, got {best_fit}")
if values["baseline_small"] != 0 or values["best_fit_small"] != 1:
    raise SystemExit(f"unexpected 1-GPU placement: {values}")
print(f"verified GPU allocation metrics: baseline={baseline:.2f}%, best-fit={best_fit:.2f}%")
PY

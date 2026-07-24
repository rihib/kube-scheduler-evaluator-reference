# kube-scheduler-evaluator Reference Implementation

kube-scheduler-evaluator-reference enables you to run reference scenarios locally using [kube-scheduler-evaluator](https://github.com/pfnet/kube-scheduler-evaluator) and docker-compose.

## Quick Start

```bash
git clone https://github.com/pfnet/kube-scheduler-evaluator-reference.git
cd kube-scheduler-evaluator-reference

make  # Up and run evaluation
make open  # Grafana dashboard; url: http://localhost:3000, user: admin, password: password

make clean # Stop and clean up
```

## KubeCon Japan GPU bin-packing demo

The demo runs a deterministic synthetic scenario at Alibaba GPU 2023 trace
scale: 1,523 nodes and 8,152 Pods. Every Pod requests at least one GPU; the demo
does not download or replay the Alibaba trace. The synthetic cluster has 6,212
allocatable GPUs across 1-, 4-, and 8-GPU nodes, plus CPU-only nodes.

The baseline plugin prefers the node with the highest current GPU allocation
percentage. The best-fit plugin instead prefers the feasible node with the
fewest GPUs left after placing the incoming Pod. Both plugins receive the same
events spread across 150 days. Regular jobs run from six hours to 14 days. A
late comparison window adds 60-day 1-GPU jobs followed by 20-day 8-GPU jobs,
making GPU fragmentation visible without concentrating thousands of creations
at one virtual timestamp. The planned workload spans 160 days.

```bash
git clone --branch agent/kubecon-gpu-binpacking-demo https://github.com/rihib/kube-scheduler-evaluator.git
git clone --branch agent/kubecon-gpu-binpacking-demo https://github.com/rihib/kube-scheduler-evaluator-reference.git
cd kube-scheduler-evaluator-reference

make demo
make open
```

If the repositories already exist, `git pull` alone does not switch branches.
Switch both checkouts explicitly before running the demo:

```bash
git -C ../kube-scheduler-evaluator fetch origin agent/kubecon-gpu-binpacking-demo
git -C ../kube-scheduler-evaluator switch agent/kubecon-gpu-binpacking-demo
git -C ../kube-scheduler-evaluator pull --ff-only

git fetch origin agent/kubecon-gpu-binpacking-demo
git switch agent/kubecon-gpu-binpacking-demo
git pull --ff-only
```

Open the **Kube Scheduler Evaluator / GPU Bin Packing Demo** dashboard. Select
the newest `evaluation_id` if metrics from earlier runs remain in the persistent
VictoriaMetrics volume. The dashboard shows the cluster GPU allocation curve,
the time-weighted average allocation, and the workload completion time for
`scenario-gpu-utilization-binpack` and `scenario-gpu-best-fit-binpack`.
`make demo` verifies that all three metrics are available for both replays
before returning.

The dashboard calls this *GPU allocation*, not physical GPU utilization. The
evaluator observes scheduler-visible Pod requests and node allocatable capacity;
hardware activity would require a runtime telemetry source such as DCGM.

For a live presentation, keep Grafana open before running `make demo`. Refresh
the dashboard after the command prints `verified GPU allocation metrics`, then
walk through the allocation curve, average, and completion-time panels. The demo
uses approximately 160 days of virtual time and requires no trace download.

## Scenarios

The kube-scheduler-evaluator-reference implements the following scenarios:

- example: A simple scenario that includes an explanation of how to create your own scenario.
- clusterdata: A scenario for replaying trace data from [alibaba/clusterdata](https://github.com/alibaba/clusterdata).

## `externalMode`

To enable `externalMode`, pass `true` as the 5th argument to the `evaluator.WithConfig` function.

```bash
make ext  # Up and run evaluation in externalMode
make open
make clean
```

## 謝辞

この成果は、国立研究開発法人新エネルギー・産業技術総合開発機構（ＮＥＤＯ）の
「ポスト５Ｇ情報通信システム基盤強化研究開発事業」（JPNP20017）の委託事業の結果得られたものです。

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

The demo compares two Score plugins on a heterogeneous cluster with a 1-GPU
node and an 8-GPU node. A 4-GPU seed Pod first occupies the large node. The
baseline plugin scores the nodes by their current GPU allocation percentage,
so it places the next 1-GPU Pod on the 50%-allocated large node. This leaves
only three contiguous GPUs there, and a subsequent 4-GPU Pod must wait.

The improved plugin uses best-fit: it prefers the feasible node with the
fewest GPUs remaining after placement. It therefore fills the 1-GPU node and
preserves four contiguous GPUs on the large node, allowing the final Pod to
start immediately.

```bash
git clone https://github.com/rihib/kube-scheduler-evaluator.git
git clone https://github.com/rihib/kube-scheduler-evaluator-reference.git
cd kube-scheduler-evaluator-reference

make demo
make open
```

Open the **Kube Scheduler Evaluator / GPU Bin Packing Demo** dashboard. Select
the newest `evaluation_id` if metrics from earlier runs remain in the persistent
VictoriaMetrics volume. The expected peak cluster GPU allocation is 55.56% for
`scenario-gpu-utilization-binpack` and 100% for
`scenario-gpu-best-fit-binpack`; `make demo` verifies this automatically before
returning.

The dashboard calls this *GPU allocation*, not physical GPU utilization. The
evaluator observes scheduler-visible Pod requests and node allocatable capacity;
hardware activity would require a runtime telemetry source such as DCGM.

For a live presentation, keep Grafana open before running `make demo`. Refresh
the dashboard after the command prints `verified GPU allocation metrics`, then
walk through the cluster allocation panel and the per-node free-GPU panel. The
demo uses virtual timestamps, so the dashboard time range intentionally extends
60 minutes into the future.

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

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

## KubeCon demo: Alibaba GPU 2023

The KubeCon dashboard replays the Alibaba GPU 2023 production trace:

- 1,523 heterogeneous cluster nodes
- 8,152 tasks
- CPU request utilization and GPU `gpu_milli` request utilization over wall-clock time
- wall-clock completion time, simulated time span, and simulation speedup

Cache the trace once before traveling so that the demo does not depend on venue Wi-Fi:

```bash
make demo-prepare
```

Run the complete demo and open its provisioned Grafana dashboard:

```bash
make demo
```

For a staged presentation, use separate terminals:

```bash
# Terminal 1
make down up
make demo-run

# Terminal 2, immediately after demo-run starts
make demo-open
```

Grafana is available only on `127.0.0.1:3000` with anonymous Viewer access.
VictoriaMetrics is available only on `127.0.0.1:8428`.
The CPU and GPU panels show scheduler-visible resource requests, not physical
device telemetry; the evaluator does not execute the workloads.

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

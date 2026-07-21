# kube-scheduler-evaluator Reference Implementation

kube-scheduler-evaluator-reference enables you to run reference scenarios locally using [kube-scheduler-evaluator](https://github.com/pfnet/kube-scheduler-evaluator) and docker-compose.

## Quick Start

This repository is checked out as a git submodule of
[kube-scheduler-evaluator](https://github.com/rihib/kube-scheduler-evaluator)
(`go.mod` resolves the evaluator from the parent directory, so changes to the
evaluator are picked up immediately).

```bash
git clone --recurse-submodules https://github.com/rihib/kube-scheduler-evaluator.git
cd kube-scheduler-evaluator/kube-scheduler-evaluator-reference

make  # Up and run evaluation
make open  # Grafana dashboard; url: http://localhost:3000, user: admin, password: password

make clean # Stop and clean up
```

## Scenarios

The kube-scheduler-evaluator-reference implements the following scenarios:

- example: A simple scenario that includes an explanation of how to create your own scenario.
- clusterdata: A scenario for replaying trace data from [alibaba/clusterdata](https://github.com/alibaba/clusterdata).
- gpubinpacking: The GPU bin-packing demo. Two custom score plugins
  (`GPUUtilizationBinPacking` and `GPUFreeCountBinPacking` under
  `plugins/gpubinpacking/`) replay the same workload on a cluster whose nodes
  have heterogeneous GPU counts, and the provisioned "GPU Bin-Packing Demo"
  Grafana dashboard compares the resulting cluster-wide GPU utilization.

## GPU Bin-Packing Demo

```bash
make demo  # Up and run both demo scenarios back to back
make open  # Grafana: open the "GPU Bin-Packing Demo" dashboard

# Or run the scenarios one at a time while presenting:
make up
make demo-utilization  # baseline:  packs by GPU utilization %  (~83% peak, jobs left pending)
make demo-freecount    # improved:  packs by free GPU count     (100% peak, nothing pending)
```

Events are paced at 500ms of real time so the dashboard draws live; override
with `DEMO_PACE` (e.g. `DEMO_PACE=0s make demo`). See
[docs/kubecon-demo.md](https://github.com/rihib/kube-scheduler-evaluator/blob/main/docs/kubecon-demo.md)
in the parent repository for the full demo walkthrough.

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

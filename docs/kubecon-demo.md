# KubeCon demo runbook

## What the audience should see

The dashboard tells one story:

1. The input is a production-scale trace: 1,523 nodes and 8,152 tasks.
2. Its virtual span is about 162 days.
3. kube-scheduler-evaluator replays it in tens of seconds.
4. CPU and GPU request-utilization curves change on the evaluator's virtual
   timeline while the default scheduler places and completes the tasks.

The GPU curve uses Alibaba's `gpu_milli` field. For a GPU-sharing task, this is
the requested fraction of one GPU. For multi-GPU tasks, the dashboard counts
`num_gpu * gpu_milli`.

## Before the conference

```bash
make demo-prepare
make demo
```

Confirm that all eight panels have data. Keep the `.cache/clusterdata`
directory; it is intentionally not removed by `make clean`.

## On stage

Terminal 1:

```bash
make down up
make demo-run
```

As soon as the replay starts, open the dashboard from Terminal 2:

```bash
make demo-open
```

`make demo-open` selects the newest evaluation and its exact virtual-time range,
so previously selected Grafana state does not affect the demo.

Suggested narration:

> This is not a synthetic ten-pod example. It is Alibaba's 2023 GPU trace:
> 1,523 heterogeneous nodes and 8,152 tasks over roughly 162 days of virtual
> time. The lines are scheduler-visible CPU and fractional-GPU requests. The
> evaluator keeps the ordering and duration semantics in virtual time, but the
> complete run finishes here in tens of seconds.

## Recovery

- Empty dashboard: run `make open` again. It derives the newest evaluation and
  its time range directly from VictoriaMetrics.
- No Grafana: run `docker compose -f docker/compose.yaml ps`, then
  `make down up`.
- Missing CSV: rerun `make demo-prepare` while online.
- Keep the terminal quiet on stage: the dedicated `cmd/kubecon` binary
  suppresses per-object evaluator logs; a non-zero exit still makes `make`
  fail.

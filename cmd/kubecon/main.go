package main

import (
	"context"
	"io"
	"log/slog"
	"os"

	"github.com/pfnet/kube-scheduler-evaluator-reference/scenarios/clusterdata"
	"github.com/pfnet/kube-scheduler-evaluator-reference/scenarios/consts"
	"github.com/pfnet/kube-scheduler-evaluator/cmd/evaluator"
	"github.com/pfnet/kube-scheduler-evaluator/pkg/definition"
	"k8s.io/klog/v2"
)

func main() {
	klog.SetOutput(io.Discard)
	eval, err := evaluator.New(
		evaluator.WithConfig(
			consts.UserID,
			consts.KubeconfigPath,
			consts.EtcdPrefix,
			consts.EtcdURL,
			false,
			slog.LevelError+4,
		),
		evaluator.WithMetricStores(
			definition.NewMetricStore(definition.VictoriaMetrics, "http://localhost:8428"),
		),
		evaluator.WithScenario(clusterdata.Name, 1, clusterdata.Generator, nil),
	)
	if err != nil {
		panic(err)
	}
	os.Exit(eval.Run(context.Background()))
}

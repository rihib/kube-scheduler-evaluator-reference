package main

import (
	"context"
	"os"

	"github.com/pfnet/kube-scheduler-evaluator-reference/scenarios/clusterdata"
	"github.com/pfnet/kube-scheduler-evaluator-reference/scenarios/consts"
	"github.com/pfnet/kube-scheduler-evaluator-reference/scenarios/example"
	"github.com/pfnet/kube-scheduler-evaluator/cmd/evaluator"
	"github.com/pfnet/kube-scheduler-evaluator/pkg/definition"
)

func main() {
	eval, err := evaluator.New(
		evaluator.WithConfig(
			consts.UserID,
			consts.KubeconfigPath,
			consts.EtcdPrefix,
			consts.EtcdURL,
			false, // disable externalMode
			consts.SlogLevel,
		),
		evaluator.WithMetricStores(
			definition.NewMetricStore(definition.VictoriaMetrics, "http://localhost:8428"),
		),
		// scenarioName, iteration, scenarioGenerator, schedulerRegistry, schedulerOptions
		evaluator.WithScenario(example.Name, 3, example.Generator, nil),
		evaluator.WithScenario(clusterdata.Name, 1, clusterdata.Generator, nil), // alibaba clusterdata gpu2023
	)
	if err != nil {
		panic(err)
	}
	code := eval.Run(context.Background())
	os.Exit(code)
}

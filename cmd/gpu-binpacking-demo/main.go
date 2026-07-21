package main

import (
	"context"
	"os"

	"github.com/pfnet/kube-scheduler-evaluator-reference/plugins/gpubinpacking"
	"github.com/pfnet/kube-scheduler-evaluator-reference/scenarios/consts"
	demoscenario "github.com/pfnet/kube-scheduler-evaluator-reference/scenarios/gpubinpacking"
	"github.com/pfnet/kube-scheduler-evaluator/cmd/evaluator"
	"github.com/pfnet/kube-scheduler-evaluator/pkg/definition"
	"k8s.io/kubernetes/pkg/scheduler"
	schedulerapi "k8s.io/kubernetes/pkg/scheduler/apis/config"
	"k8s.io/kubernetes/pkg/scheduler/framework/runtime"
)

func main() {
	metricsURL := os.Getenv("VICTORIAMETRICS_URL")
	if metricsURL == "" {
		metricsURL = "http://localhost:8428"
	}
	registry := runtime.Registry{
		gpubinpacking.UtilizationName: gpubinpacking.NewUtilization,
		gpubinpacking.BestFitName:     gpubinpacking.NewBestFit,
	}
	eval, err := evaluator.New(
		evaluator.WithConfig(
			consts.UserID,
			consts.KubeconfigPath,
			consts.EtcdPrefix,
			consts.EtcdURL,
			false,
			consts.SlogLevel,
		),
		evaluator.WithMetricStores(
			definition.NewMetricStore(definition.VictoriaMetrics, metricsURL),
		),
		evaluator.WithScenario(
			demoscenario.UtilizationScenarioName,
			1,
			demoscenario.UtilizationGenerator,
			registry,
			scheduler.WithProfiles(profile(demoscenario.UtilizationScheduler, gpubinpacking.UtilizationName)),
		),
		evaluator.WithScenario(
			demoscenario.BestFitScenarioName,
			1,
			demoscenario.BestFitGenerator,
			registry,
			scheduler.WithProfiles(profile(demoscenario.BestFitScheduler, gpubinpacking.BestFitName)),
		),
	)
	if err != nil {
		panic(err)
	}
	os.Exit(eval.Run(context.Background()))
}

func profile(schedulerName, scorePlugin string) schedulerapi.KubeSchedulerProfile {
	return schedulerapi.KubeSchedulerProfile{
		SchedulerName: schedulerName,
		PluginConfig: []schedulerapi.PluginConfig{{
			Name: "NodeResourcesFit",
			Args: &schedulerapi.NodeResourcesFitArgs{
				ScoringStrategy: &schedulerapi.ScoringStrategy{
					Type: schedulerapi.LeastAllocated,
					Resources: []schedulerapi.ResourceSpec{
						{Name: "cpu", Weight: 1},
						{Name: "memory", Weight: 1},
					},
				},
			},
		}},
		Plugins: &schedulerapi.Plugins{
			QueueSort: schedulerapi.PluginSet{
				Enabled: []schedulerapi.Plugin{{Name: "PrioritySort"}},
			},
			PreFilter: schedulerapi.PluginSet{
				Enabled: []schedulerapi.Plugin{{Name: "NodeResourcesFit"}},
			},
			Filter: schedulerapi.PluginSet{
				Enabled: []schedulerapi.Plugin{{Name: "NodeResourcesFit"}},
			},
			Score: schedulerapi.PluginSet{
				Enabled: []schedulerapi.Plugin{{Name: scorePlugin, Weight: 1}},
			},
			Bind: schedulerapi.PluginSet{
				Enabled: []schedulerapi.Plugin{{Name: "DefaultBinder"}},
			},
		},
	}
}

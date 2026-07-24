package main

import (
	"context"
	"fmt"
	"os"

	gpubinpackingplugins "github.com/pfnet/kube-scheduler-evaluator-reference/plugins/gpubinpacking"
	"github.com/pfnet/kube-scheduler-evaluator-reference/scenarios/clusterdata"
	"github.com/pfnet/kube-scheduler-evaluator-reference/scenarios/consts"
	"github.com/pfnet/kube-scheduler-evaluator-reference/scenarios/example"
	"github.com/pfnet/kube-scheduler-evaluator-reference/scenarios/gpubinpacking"
	"github.com/pfnet/kube-scheduler-evaluator/cmd/evaluator"
	"github.com/pfnet/kube-scheduler-evaluator/pkg/definition"
	"k8s.io/kubernetes/pkg/scheduler"
	schedulerapi "k8s.io/kubernetes/pkg/scheduler/apis/config"
	"k8s.io/kubernetes/pkg/scheduler/apis/config/scheme"
	"k8s.io/kubernetes/pkg/scheduler/framework/runtime"
)

const gpuBinpackingSchedulerConfigPath = "config/gpu-binpacking-scheduler/scheduler-config.yaml"

func main() {
	scenarios, err := scenariosFor(os.Getenv("SCENARIOS"))
	if err != nil {
		panic(err)
	}
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
		scenarios...,
	)
	if err != nil {
		panic(err)
	}
	code := eval.Run(context.Background())
	os.Exit(code)
}

// scenariosFor selects the scenario set via the SCENARIOS environment variable:
//   - "demo":             both GPU bin-packing demo scenarios with the custom scheduler plugins
//   - "demo-utilization": only the utilization-percentage bin-packing scenario
//   - "demo-freecount":   only the free-GPU-count bin-packing scenario
//   - anything else:      the default reference scenarios
func scenariosFor(set string) ([]definition.Scenario, error) {
	switch set {
	case "demo", "demo-utilization", "demo-freecount":
		registry := runtime.Registry{
			gpubinpackingplugins.UtilizationName: gpubinpackingplugins.NewUtilization,
			gpubinpackingplugins.FreeCountName:   gpubinpackingplugins.NewFreeCount,
		}
		profiles, err := loadProfiles(gpuBinpackingSchedulerConfigPath)
		if err != nil {
			return nil, err
		}
		// scenarioName, iteration, scenarioGenerator, schedulerRegistry, schedulerOptions
		utilization := evaluator.WithScenario(
			gpubinpacking.UtilizationScenarioName, 1, gpubinpacking.UtilizationGenerator,
			registry, scheduler.WithProfiles(profiles...),
		)
		freecount := evaluator.WithScenario(
			gpubinpacking.FreeCountScenarioName, 1, gpubinpacking.FreeCountGenerator,
			registry, scheduler.WithProfiles(profiles...),
		)
		switch set {
		case "demo-utilization":
			return []definition.Scenario{utilization}, nil
		case "demo-freecount":
			return []definition.Scenario{freecount}, nil
		default:
			return []definition.Scenario{utilization, freecount}, nil
		}
	}
	return []definition.Scenario{
		// scenarioName, iteration, scenarioGenerator, schedulerRegistry, schedulerOptions
		evaluator.WithScenario(example.Name, 3, example.Generator, nil),
		evaluator.WithScenario(clusterdata.Name, 1, clusterdata.Generator, nil), // alibaba clusterdata gpu2023
	}, nil
}

func loadProfiles(path string) ([]schedulerapi.KubeSchedulerProfile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("loadProfiles: %w", err)
	}
	obj, gvk, err := scheme.Codecs.UniversalDecoder().Decode(data, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("loadProfiles: failed to decode %s: %w", path, err)
	}
	cfg, ok := obj.(*schedulerapi.KubeSchedulerConfiguration)
	if !ok {
		return nil, fmt.Errorf("loadProfiles: unexpected object type %s in %s", gvk, path)
	}
	return cfg.Profiles, nil
}

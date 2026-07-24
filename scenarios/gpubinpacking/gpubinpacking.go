package gpubinpacking

import (
	"github.com/pfnet/kube-scheduler-evaluator/pkg/definition"
)

const (
	UtilizationScenarioName = "gpu-utilization-binpack"
	BestFitScenarioName     = "gpu-best-fit-binpack"
	UtilizationScheduler    = "gpu-utilization-scheduler"
	BestFitScheduler        = "gpu-best-fit-scheduler"
)

func UtilizationGenerator(ch chan<- definition.Event) {
	generate(ch, UtilizationScheduler)
}

func BestFitGenerator(ch chan<- definition.Event) {
	generate(ch, BestFitScheduler)
}

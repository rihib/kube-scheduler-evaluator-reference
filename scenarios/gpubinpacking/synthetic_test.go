package gpubinpacking

import (
	"testing"

	"github.com/pfnet/kube-scheduler-evaluator/pkg/definition"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	resourcehelper "k8s.io/component-helpers/resource"
)

func TestSyntheticScenarioScaleAndGPURequests(t *testing.T) {
	events := make(chan definition.Event, NodeCount+PodCount)
	generate(events, UtilizationScheduler)
	close(events)

	var nodeCount, podCount int
	var allocatableGPU, requestedGPU int64
	for scenarioEvent := range events {
		switch obj := scenarioEvent.Object().(type) {
		case *corev1.Node:
			nodeCount++
			gpus := obj.Status.Allocatable[corev1.ResourceName("nvidia.com/gpu")]
			allocatableGPU += gpus.Value()
		case *appsv1.ReplicaSet:
			podCount++
			pod := &corev1.Pod{Spec: obj.Spec.Template.Spec}
			requests := resourcehelper.PodRequests(pod, resourcehelper.PodResourcesOptions{})
			gpus := requests[corev1.ResourceName("nvidia.com/gpu")]
			if gpus.Sign() <= 0 {
				t.Fatalf("Pod %q does not request a GPU", obj.Name)
			}
			requestedGPU += gpus.Value()
		default:
			t.Fatalf("unexpected scenario object type %T", obj)
		}
	}

	if nodeCount != NodeCount {
		t.Fatalf("node count = %d, want %d", nodeCount, NodeCount)
	}
	if podCount != PodCount {
		t.Fatalf("Pod count = %d, want %d", podCount, PodCount)
	}
	if allocatableGPU != 6212 {
		t.Fatalf("allocatable GPUs = %d, want 6212", allocatableGPU)
	}
	if requestedGPU <= 0 {
		t.Fatal("total requested GPUs must be positive")
	}
}

func TestGeneratorsUseTheirSchedulerProfiles(t *testing.T) {
	tests := []struct {
		name      string
		generator func(chan<- definition.Event)
		want      string
	}{
		{name: "utilization", generator: UtilizationGenerator, want: UtilizationScheduler},
		{name: "best-fit", generator: BestFitGenerator, want: BestFitScheduler},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch := make(chan definition.Event, NodeCount+PodCount)
			tt.generator(ch)
			close(ch)

			for scenarioEvent := range ch {
				replicaSet, ok := scenarioEvent.Object().(*appsv1.ReplicaSet)
				if !ok {
					continue
				}
				if got := replicaSet.Spec.Template.Spec.SchedulerName; got != tt.want {
					t.Fatalf("scheduler name = %q, want %q", got, tt.want)
				}
				return
			}
			t.Fatal("generator produced no ReplicaSet")
		})
	}
}

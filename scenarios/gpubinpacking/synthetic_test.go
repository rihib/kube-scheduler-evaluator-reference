package gpubinpacking

import (
	"sort"
	"testing"
	"time"

	"github.com/pfnet/kube-scheduler-evaluator/pkg/definition"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	resourcehelper "k8s.io/component-helpers/resource"
)

func TestSyntheticScenarioScaleAndGPURequests(t *testing.T) {
	events := make(chan definition.Event, NodeCount*2+PodCount)
	generate(events, UtilizationScheduler)
	close(events)

	var createdNodeCount, deletedNodeCount, podCount int
	var allocatableGPU, requestedGPU int64
	var podCreation, lastPodCreation, lastPodCompletion time.Duration
	var firstNodeDeletionInterval time.Duration
	var longRunningPods, blockerPods, largePods int
	var nodeDeletionBeforeAllPods bool
	var lifetimes []lifetime
	for scenarioEvent := range events {
		switch obj := scenarioEvent.Object().(type) {
		case *corev1.Node:
			switch scenarioEvent.EventType() {
			case definition.EventTypeCreate:
				createdNodeCount++
				gpus := obj.Status.Allocatable[corev1.ResourceName("nvidia.com/gpu")]
				allocatableGPU += gpus.Value()
			case definition.EventTypeDelete:
				if podCount != PodCount {
					nodeDeletionBeforeAllPods = true
				}
				if deletedNodeCount == 0 {
					firstNodeDeletionInterval = scenarioEvent.Interval()
				}
				deletedNodeCount++
			default:
				t.Fatalf("unexpected Node event type %v", scenarioEvent.EventType())
			}
		case *appsv1.ReplicaSet:
			if scenarioEvent.EventType() != definition.EventTypeCreate {
				t.Fatalf("unexpected ReplicaSet event type %v", scenarioEvent.EventType())
			}
			podCount++
			podCreation += scenarioEvent.Interval()
			lastPodCreation = podCreation
			pod := &corev1.Pod{Spec: obj.Spec.Template.Spec}
			requests := resourcehelper.PodRequests(pod, resourcehelper.PodResourcesOptions{})
			gpus := requests[corev1.ResourceName("nvidia.com/gpu")]
			if gpus.Sign() <= 0 {
				t.Fatalf("Pod %q does not request a GPU", obj.Name)
			}
			requestedGPU += gpus.Value()
			rawDuration := obj.Spec.Template.Annotations[definition.ExecutionDurationAnnotationKey("example.com")]
			duration, err := time.ParseDuration(rawDuration)
			if err != nil {
				t.Fatalf("Pod %q has invalid duration %q: %v", obj.Name, rawDuration, err)
			}
			if duration >= 24*time.Hour {
				longRunningPods++
			}
			if duration == blockerPodDuration {
				blockerPods++
			}
			if gpus.Value() == 8 && duration == largePodDuration {
				largePods++
			}
			lastPodCompletion = max(lastPodCompletion, podCreation+duration)
			lifetimes = append(lifetimes, lifetime{start: podCreation, end: podCreation + duration})
		default:
			t.Fatalf("unexpected scenario object type %T", obj)
		}
	}

	if createdNodeCount != NodeCount {
		t.Fatalf("created node count = %d, want %d", createdNodeCount, NodeCount)
	}
	if deletedNodeCount != NodeCount {
		t.Fatalf("deleted node count = %d, want %d", deletedNodeCount, NodeCount)
	}
	if nodeDeletionBeforeAllPods {
		t.Fatal("node deletion occurred before all Pods were submitted")
	}
	if want := nodeCleanupAt - podCreationSpan; firstNodeDeletionInterval != want {
		t.Fatalf("first node deletion interval = %v, want %v", firstNodeDeletionInterval, want)
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
	if lastPodCreation != podCreationSpan {
		t.Fatalf("last Pod creation = %v, want %v", lastPodCreation, podCreationSpan)
	}
	if lastPodCompletion != scenarioDuration {
		t.Fatalf("last planned Pod completion = %v, want %v", lastPodCompletion, scenarioDuration)
	}
	if longRunningPods == 0 {
		t.Fatal("scenario has no Pods running for at least one day")
	}
	if blockerPods != blockerPodCount {
		t.Fatalf("60-day blocker Pods = %d, want %d", blockerPods, blockerPodCount)
	}
	if largePods != largePodCount {
		t.Fatalf("20-day 8-GPU Pods = %d, want %d", largePods, largePodCount)
	}
	if active := maximumConcurrentPods(lifetimes); active > 1500 {
		t.Fatalf("planned concurrent Pods = %d, want at most 1500", active)
	}
}

type lifetime struct {
	start time.Duration
	end   time.Duration
}

func maximumConcurrentPods(lifetimes []lifetime) int {
	type boundary struct {
		at    time.Duration
		delta int
	}
	boundaries := make([]boundary, 0, len(lifetimes)*2)
	for _, item := range lifetimes {
		boundaries = append(boundaries, boundary{at: item.start, delta: 1})
		boundaries = append(boundaries, boundary{at: item.end, delta: -1})
	}
	sort.Slice(boundaries, func(i, j int) bool {
		if boundaries[i].at == boundaries[j].at {
			return boundaries[i].delta < boundaries[j].delta
		}
		return boundaries[i].at < boundaries[j].at
	})
	active, maximum := 0, 0
	for _, item := range boundaries {
		active += item.delta
		maximum = max(maximum, active)
	}
	return maximum
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
			ch := make(chan definition.Event, NodeCount*2+PodCount)
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

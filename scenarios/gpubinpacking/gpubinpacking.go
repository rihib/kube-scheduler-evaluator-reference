package gpubinpacking

import (
	"fmt"
	"time"

	"github.com/pfnet/kube-scheduler-evaluator-reference/scenarios/consts"
	"github.com/pfnet/kube-scheduler-evaluator/pkg/definition"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	UtilizationScenarioName = "gpu-utilization-binpack"
	BestFitScenarioName     = "gpu-best-fit-binpack"
	UtilizationScheduler    = "gpu-utilization-scheduler"
	BestFitScheduler        = "gpu-best-fit-scheduler"
)

const (
	eventInterval = time.Minute
	jobDuration   = 30 * time.Minute
	largeDuration = 20 * time.Minute
	jobDeadline   = 2 * time.Hour
)

var gpuResourceName = corev1.ResourceName("nvidia.com/gpu")

func UtilizationGenerator(ch chan<- definition.Event) {
	generate(ch, UtilizationScheduler)
}

func BestFitGenerator(ch chan<- definition.Event) {
	generate(ch, BestFitScheduler)
}

func generate(ch chan<- definition.Event, schedulerName string) {
	// A failed GPU can leave otherwise identical nodes with different capacity.
	smallNode := node("gpu-small", 1)
	largeNode := node("gpu-large", 8)
	ch <- definition.NewEvent(definition.EventTypeCreate, smallNode, time.Second)
	ch <- definition.NewEvent(definition.EventTypeCreate, largeNode, time.Second)

	// Only gpu-large can accept this seed Pod, leaving it at 50% allocation.
	ch <- definition.NewEvent(definition.EventTypeCreate, pod("seed-4gpu", schedulerName, 4, jobDuration), eventInterval)
	// The competing scorers make different decisions for this 1-GPU Pod.
	ch <- definition.NewEvent(definition.EventTypeCreate, pod("filler-1gpu", schedulerName, 1, jobDuration), eventInterval)
	// This Pod exposes the consequence: it waits with the utilization scorer,
	// but starts immediately when best-fit preserved four contiguous GPUs.
	ch <- definition.NewEvent(definition.EventTypeCreate, pod("workload-4gpu", schedulerName, 4, largeDuration), eventInterval)

	// Scenarios share the evaluator's in-memory API server. Remove the fixed-name
	// nodes after all Pods have completed so the second comparison starts from
	// exactly the same cluster state.
	ch <- definition.NewEvent(definition.EventTypeDelete, smallNode, 2*time.Hour)
	ch <- definition.NewEvent(definition.EventTypeDelete, largeNode, time.Second)
}

func node(name string, gpu int64) *corev1.Node {
	resources := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("64"),
		corev1.ResourceMemory: resource.MustParse("256Gi"),
		corev1.ResourcePods:   resource.MustParse("110"),
		gpuResourceName:       *resource.NewQuantity(gpu, resource.DecimalSI),
	}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			Capacity:    resources.DeepCopy(),
			Allocatable: resources.DeepCopy(),
		},
	}
	node.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Node"))
	return node
}

func pod(name, schedulerName string, gpu int64, execution time.Duration) *corev1.Pod {
	requests := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("1"),
		corev1.ResourceMemory: resource.MustParse("1Gi"),
		gpuResourceName:       *resource.NewQuantity(gpu, resource.DecimalSI),
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Annotations: map[string]string{
				definition.DeadlineDurationAnnotationKey(consts.UserID):  jobDeadline.String(),
				definition.ExecutionDurationAnnotationKey(consts.UserID): execution.String(),
			},
			Labels: map[string]string{"demo": "gpu-binpacking"},
		},
		Spec: corev1.PodSpec{
			SchedulerName: schedulerName,
			Containers: []corev1.Container{{
				Name:  fmt.Sprintf("request-%dgpu", gpu),
				Image: "registry.example.com/gpu-job:demo",
				Resources: corev1.ResourceRequirements{
					Requests: requests.DeepCopy(),
					Limits:   requests.DeepCopy(),
				},
			}},
		},
	}
	pod.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Pod"))
	return pod
}

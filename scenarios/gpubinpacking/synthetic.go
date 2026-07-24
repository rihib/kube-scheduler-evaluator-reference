package gpubinpacking

import (
	"fmt"
	"maps"
	"time"

	"github.com/pfnet/kube-scheduler-evaluator-reference/scenarios/consts"
	"github.com/pfnet/kube-scheduler-evaluator/pkg/definition"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

const (
	NodeCount = 1523
	PodCount  = 8152

	eightGPUNodeCount = 400
	fourGPUNodeCount  = 733
	oneGPUNodeCount   = 80

	blockerPodStart = 4348
	blockerPodCount = 800
	largePodStart   = blockerPodStart + blockerPodCount
	largePodCount   = 390

	nodeInterval       = time.Millisecond
	podCreationSpan    = 150 * 24 * time.Hour
	scenarioDuration   = 160 * 24 * time.Hour
	blockerPodDuration = 60 * 24 * time.Hour
	largePodDuration   = 20 * 24 * time.Hour
)

var (
	regularGPURequests  = [...]int64{1, 1, 1, 2, 2, 4, 1}
	regularPodDurations = [...]time.Duration{
		6 * time.Hour,
		12 * time.Hour,
		24 * time.Hour,
		48 * time.Hour,
		72 * time.Hour,
		7 * 24 * time.Hour,
		14 * 24 * time.Hour,
	}
)

func generate(ch chan<- definition.Event, schedulerName string) {
	for i := range NodeCount {
		ch <- definition.NewEvent(
			definition.EventTypeCreate,
			buildSyntheticNode(i, gpuCapacityForNode(i)),
			nodeInterval,
		)
	}

	previousCreation := time.Duration(0)
	for podIndex := range PodCount {
		workload := syntheticWorkloadAt(podIndex)
		interval := workload.creation - previousCreation
		ch <- newSyntheticPodEvent(
			schedulerName,
			podIndex,
			workload.gpuCount,
			workload.duration,
			interval,
		)
		previousCreation = workload.creation
	}
}

type syntheticWorkload struct {
	creation time.Duration
	duration time.Duration
	gpuCount int64
}

func syntheticWorkloadAt(index int) syntheticWorkload {
	segments := time.Duration(PodCount - 1)
	creation := time.Duration(index)*(podCreationSpan/segments) +
		time.Duration(index)*(podCreationSpan%segments)/segments
	workload := syntheticWorkload{
		creation: creation,
		duration: regularPodDurations[index%len(regularPodDurations)],
		gpuCount: regularGPURequests[index%len(regularGPURequests)],
	}
	switch {
	case index >= blockerPodStart && index < blockerPodStart+blockerPodCount:
		workload.duration = blockerPodDuration
		workload.gpuCount = 1
	case index >= largePodStart && index < largePodStart+largePodCount:
		workload.duration = largePodDuration
		workload.gpuCount = 8
	}

	remaining := scenarioDuration - creation
	if workload.duration > remaining {
		workload.duration = remaining
	}
	if index == PodCount-1 {
		workload.duration = remaining
	}
	return workload
}

func gpuCapacityForNode(index int) int64 {
	switch {
	case index < eightGPUNodeCount:
		return 8
	case index < eightGPUNodeCount+fourGPUNodeCount:
		return 4
	case index < eightGPUNodeCount+fourGPUNodeCount+oneGPUNodeCount:
		return 1
	default:
		return 0
	}
}

func buildSyntheticNode(index int, gpuCount int64) *corev1.Node {
	capacity := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("64"),
		corev1.ResourceMemory: resource.MustParse("256Gi"),
		corev1.ResourcePods:   resource.MustParse("110"),
	}
	if gpuCount > 0 {
		capacity[corev1.ResourceName("nvidia.com/gpu")] = *resource.NewQuantity(gpuCount, resource.DecimalSI)
	}
	allocatable := make(corev1.ResourceList, len(capacity))
	maps.Copy(allocatable, capacity)

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("gpu-demo-node-%04d", index),
		},
		Status: corev1.NodeStatus{
			Capacity:    capacity,
			Allocatable: allocatable,
		},
	}
	node.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Node"))
	return node
}

func newSyntheticPodEvent(
	schedulerName string,
	index int,
	gpuCount int64,
	duration time.Duration,
	interval time.Duration,
) definition.Event {
	name := fmt.Sprintf("gpu-demo-pod-%04d", index)
	labels := map[string]string{"gpu-demo-pod": name}
	replicaSet := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Annotations: map[string]string{
				definition.DeadlineDurationAnnotationKey(consts.UserID): (duration * 2).String(),
			},
		},
		Spec: appsv1.ReplicaSetSpec{
			Replicas: ptr.To[int32](1),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
					Annotations: map[string]string{
						definition.ExecutionDurationAnnotationKey(consts.UserID): duration.String(),
					},
				},
				Spec: corev1.PodSpec{
					SchedulerName: schedulerName,
					Containers: []corev1.Container{{
						Name:  "gpu-job",
						Image: "registry.example.com/gpu-job:1.0",
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:                    resource.MustParse("100m"),
								corev1.ResourceMemory:                 resource.MustParse("128Mi"),
								corev1.ResourceName("nvidia.com/gpu"): *resource.NewQuantity(gpuCount, resource.DecimalSI),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:                    resource.MustParse("100m"),
								corev1.ResourceMemory:                 resource.MustParse("128Mi"),
								corev1.ResourceName("nvidia.com/gpu"): *resource.NewQuantity(gpuCount, resource.DecimalSI),
							},
						},
					}},
				},
			},
		},
	}
	replicaSet.SetGroupVersionKind(appsv1.SchemeGroupVersion.WithKind("ReplicaSet"))
	return definition.NewEvent(definition.EventTypeCreate, replicaSet, interval)
}

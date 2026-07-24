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

	warmupPodCount  = 5356
	blockerPodCount = 2400
	largePodCount   = 396

	nodeInterval       = time.Millisecond
	podInterval        = 10 * time.Millisecond
	phaseInterval      = time.Hour
	warmupPodDuration  = 30 * time.Minute
	blockerPodDuration = 6 * time.Hour
	largePodDuration   = time.Hour
)

func generate(ch chan<- definition.Event, schedulerName string) {
	for i := range NodeCount {
		ch <- definition.NewEvent(
			definition.EventTypeCreate,
			buildSyntheticNode(i, gpuCapacityForNode(i)),
			nodeInterval,
		)
	}

	podIndex := 0
	for range warmupPodCount {
		ch <- newSyntheticPodEvent(
			schedulerName,
			podIndex,
			1,
			warmupPodDuration,
			podInterval,
		)
		podIndex++
	}
	for i := range blockerPodCount {
		interval := podInterval
		if i == 0 {
			interval = phaseInterval
		}
		ch <- newSyntheticPodEvent(
			schedulerName,
			podIndex,
			1,
			blockerPodDuration,
			interval,
		)
		podIndex++
	}
	for range largePodCount {
		ch <- newSyntheticPodEvent(
			schedulerName,
			podIndex,
			8,
			largePodDuration,
			podInterval,
		)
		podIndex++
	}
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

package clusterdata

import (
	"fmt"
	"time"

	"github.com/pfnet/kube-scheduler-evaluator-reference/scenarios/consts"
	"github.com/pfnet/kube-scheduler-evaluator/pkg/definition"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

// buildNodeBase builds the parts of a Node common to every GPU
// representation this package builds: its name, CPU/memory/pods capacity,
// the model label, and the GVK. Callers add their own GPU capacity entries
// to node.Status.Capacity (buildNode, gpu2023.go, adds nvidia.com/gpu;
// buildInputVersionNode, input_version.go, adds the alibabacloud.com/*
// keys) and then call finalizeNodeCapacity to copy the finished Capacity
// into Allocatable.
func buildNodeBase(name string, cpuMilli, memoryMiB int64, model string) *corev1.Node {
	capacity := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse(fmt.Sprintf("%dm", cpuMilli)),
		corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%dMi", memoryMiB)),
		corev1.ResourcePods:   resource.MustParse("110"),
	}

	modelLabelKey := fmt.Sprintf("%s/model", consts.UserID)
	labels := map[string]string{}
	if model != "" {
		labels[modelLabelKey] = model
	}

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
		Status: corev1.NodeStatus{
			Capacity: capacity,
		},
	}
	node.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Node"))
	return node
}

// finalizeNodeCapacity copies node.Status.Capacity (after a caller of
// buildNodeBase has added its own GPU capacity entries) into
// node.Status.Allocatable.
func finalizeNodeCapacity(node *corev1.Node) {
	allocatable := make(corev1.ResourceList, len(node.Status.Capacity))
	for name, qty := range node.Status.Capacity {
		allocatable[name] = qty
	}
	node.Status.Allocatable = allocatable
}

// buildJobBase builds the ReplicaSet parts common to every GPU
// representation this package builds: ObjectMeta, Selector/Replicas, the
// deadline/execution-duration/qos/gpu-spec annotations, the scheduler name,
// and the container's cpu/memory Requests/Limits. Callers add their own GPU
// representation (buildJob, gpu2023.go, adds the nvidia.com/gpu extended
// resource and its own gpu-milli annotation; buildInputVersionJob,
// input_version.go, adds the alibabacloud.com/* annotations) and any
// nodeAffinity.
func buildJobBase(
	schedulerName, podName string,
	cpuMilli, memoryMiB int64,
	gpuSpec, qos string,
	executionDuration time.Duration,
) *appsv1.ReplicaSet {
	podLabels := map[string]string{"app": "example-app"}
	deadline := executionDuration * 2
	gpuSpecAnnotationKey := fmt.Sprintf("%s/gpu-spec", consts.UserID)
	qosAnnotationKey := fmt.Sprintf("%s/qos", consts.UserID)

	job := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: "default",
			Annotations: map[string]string{
				definition.DeadlineDurationAnnotationKey(consts.UserID): deadline.String(),
			},
		},
		Spec: appsv1.ReplicaSetSpec{
			Replicas: ptr.To[int32](1),
			Selector: &metav1.LabelSelector{
				MatchLabels: podLabels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: podLabels,
					Annotations: map[string]string{
						definition.ExecutionDurationAnnotationKey(consts.UserID): executionDuration.String(),
						gpuSpecAnnotationKey: gpuSpec,
						qosAnnotationKey:     qos,
					},
				},
				Spec: corev1.PodSpec{
					SchedulerName: schedulerName,
					Containers: []corev1.Container{
						{
							Name:  "example-job",
							Image: "registry.example.com/example-job:1.0",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse(fmt.Sprintf("%dm", cpuMilli)),
									corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%dMi", memoryMiB)),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse(fmt.Sprintf("%dm", cpuMilli)),
									corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%dMi", memoryMiB)),
								},
							},
						},
					},
				},
			},
		},
	}
	job.SetGroupVersionKind(appsv1.SchemeGroupVersion.WithKind("ReplicaSet"))
	return job
}

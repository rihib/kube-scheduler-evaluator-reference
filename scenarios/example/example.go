package example

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

const Name = "example"

var (
	schedulerName = "default-scheduler"
	interval      = time.Second

	// Node
	numNode = 3
	nodeCPU = 8000 // m
	nodeMem = 32   // Gi
	nodeGPU = 8

	// Pod
	numPod = 5
	podCPU = 500 // m
	podMem = 128 // Mi
	podGPU = 4

	// Job
	numJob    = 5
	deadline  = 30 * time.Minute
	execution = 1 * time.Hour
)

func Generator(ch chan<- definition.Event) {
	/*
		Create Nodes
	*/
	for range numNode {
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: fmt.Sprintf("node-%s-", Name),
			},
			Status: corev1.NodeStatus{
				Capacity: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse(fmt.Sprintf("%dm", nodeCPU)),
					corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%dGi", nodeMem)),
					corev1.ResourcePods:   resource.MustParse("110"),
					"nvidia.com/gpu":      resource.MustParse(fmt.Sprintf("%d", nodeGPU)),
				},
				Allocatable: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse(fmt.Sprintf("%dm", nodeCPU)),
					corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%dGi", nodeMem)),
					corev1.ResourcePods:   resource.MustParse("110"),
					"nvidia.com/gpu":      resource.MustParse(fmt.Sprintf("%d", nodeGPU)),
				},
			},
		}
		node.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Node")) // NOTE: GVK should be explicitly set for logging
		event := definition.NewEvent(
			definition.EventTypeCreate,
			node,
			interval,
		)
		ch <- event // NOTE: Passing one event to the channel each time it is generated helps reduce memory usage.
	}

	/*
		Create Pods
	*/
	for range numPod {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: fmt.Sprintf("pod-%s-", Name),
				Namespace:    "default",
				Annotations: map[string]string{
					definition.DeadlineDurationAnnotationKey(consts.UserID):  deadline.String(),  // Deadline for the pod
					definition.ExecutionDurationAnnotationKey(consts.UserID): execution.String(), // Execution time for the pod
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
								corev1.ResourceCPU:    resource.MustParse(fmt.Sprintf("%dm", podCPU)),
								corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%dMi", podMem)),
								"nvidia.com/gpu":      resource.MustParse(fmt.Sprintf("%d", podGPU)),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse(fmt.Sprintf("%dm", podCPU)),
								corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%dMi", podMem)),
								"nvidia.com/gpu":      resource.MustParse(fmt.Sprintf("%d", podGPU)),
							},
						},
					},
				},
			},
		}
		pod.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Pod"))
		event := definition.NewEvent(
			definition.EventTypeCreate,
			pod,
			interval,
		)
		ch <- event
	}

	/*
		Create Jobs
	*/
	// NOTE: Jobs are immediately marked as Completed by kwok, so use ReplicaSet instead.
	// Preempted Pods are recreated by the ReplicaSet.
	for range numJob {
		podLabels := map[string]string{"app": "example-app"}
		job := &appsv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: fmt.Sprintf("job-%s-", Name),
				Namespace:    "default",
				Annotations: map[string]string{
					definition.DeadlineDurationAnnotationKey(consts.UserID): deadline.String(),
				},
			},
			Spec: appsv1.ReplicaSetSpec{
				// NOTE: A replicaset can only have one Pod. Having multiple Pods is not supported.
				Replicas: ptr.To[int32](1),
				Selector: &metav1.LabelSelector{
					MatchLabels: podLabels,
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: podLabels,
						Annotations: map[string]string{
							definition.ExecutionDurationAnnotationKey(consts.UserID): execution.String(),
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
										corev1.ResourceCPU:    resource.MustParse(fmt.Sprintf("%dm", podCPU)),
										corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%dMi", podMem)),
										"nvidia.com/gpu":      resource.MustParse(fmt.Sprintf("%d", podGPU)),
									},
									Limits: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse(fmt.Sprintf("%dm", podCPU)),
										corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%dMi", podMem)),
										"nvidia.com/gpu":      resource.MustParse(fmt.Sprintf("%d", podGPU)),
									},
								},
							},
						},
					},
				},
			},
		}
		job.SetGroupVersionKind(appsv1.SchemeGroupVersion.WithKind("ReplicaSet"))
		event := definition.NewEvent(
			definition.EventTypeCreate, // EventTypeCreate, EventTypeDelete
			job,
			interval,
		)
		ch <- event
	}
}

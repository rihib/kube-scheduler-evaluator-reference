// Package gpubinpacking defines the KubeCon demo scenario: a cluster with
// heterogeneous GPU counts per node (e.g. nodes that lost GPUs to failures),
// where utilization-percentage bin packing fragments the large nodes while
// free-GPU-count bin packing keeps them packable.
//
// Timeline (both scenarios are identical; only the scheduler differs):
//
//  1. Nodes: 4 nodes with 8 GPUs and 4 nodes with 1 GPU (36 GPUs total).
//  2. Preload: 4 jobs x 5 GPUs. They only fit on the 8-GPU nodes, and 5+5 > 8,
//     so every 8-GPU node ends up with 5/8 GPUs used (62.5%) under any scheduler.
//  3. Small jobs: 4 jobs x 1 GPU.
//     - Utilization bin packing: the 8-GPU nodes (62.5%) outscore the empty
//       1-GPU nodes (0%), so the small jobs eat into the 8-GPU nodes.
//     - Free-count bin packing: the 1-GPU nodes have the fewest free GPUs,
//       so the small jobs land there and the 8-GPU nodes keep 3 free GPUs each.
//  4. Medium jobs: 4 jobs x 3 GPUs.
//     - Utilization bin packing: only 2 of them still fit; 2 stay pending and
//       the 1-GPU nodes sit idle (~83% peak GPU utilization).
//     - Free-count bin packing: all 4 fit, one per 8-GPU node
//       (100% peak GPU utilization).
package gpubinpacking

import (
	"fmt"
	"os"
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
	UtilizationScenarioName = "gpu-binpacking-utilization"
	FreeCountScenarioName   = "gpu-binpacking-freecount"

	// Scheduler profile names defined in config/gpu-binpacking-scheduler/scheduler-config.yaml
	UtilizationSchedulerName = "gpu-binpacking-utilization"
	FreeCountSchedulerName   = "gpu-binpacking-freecount"
)

var (
	interval = time.Second

	// Nodes: a cluster with heterogeneous GPU counts,
	// e.g. some nodes lost most of their GPUs to hardware failures.
	numLargeNode = 4
	largeNodeGPU = 8
	numSmallNode = 4
	smallNodeGPU = 1
	nodeCPU      = 32000 // m
	nodeMem      = 256   // Gi

	// Jobs: CPU/memory are kept small so that GPUs are always the deciding resource.
	numPreloadJob = 4
	preloadJobGPU = 5
	numSmallJob   = 4
	smallJobGPU   = 1
	numMediumJob  = 4
	mediumJobGPU  = 3
	jobCPU        = 100 // m
	jobMem        = 256 // Mi

	deadline  = 24 * time.Hour
	execution = 1 * time.Hour

	// Nodes are deleted at the end of each scenario so that consecutive
	// scenarios/iterations do not see each other's nodes. The first delete
	// event is scheduled far enough in virtual time for all jobs (including
	// ones that stayed pending and were scheduled later) to have finished.
	drainInterval = 3 * time.Hour
)

func UtilizationGenerator(ch chan<- definition.Event) {
	generate(ch, UtilizationSchedulerName)
}

func FreeCountGenerator(ch chan<- definition.Event) {
	generate(ch, FreeCountSchedulerName)
}

// pace returns the real-time delay inserted between generated events so the
// Grafana dashboard can be watched live while the demo runs. Override with
// the DEMO_PACE environment variable (a Go duration, e.g. "0s", "1s").
func pace() time.Duration {
	if v := os.Getenv("DEMO_PACE"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return 500 * time.Millisecond
}

func generate(ch chan<- definition.Event, schedulerName string) {
	p := pace()
	send := func(e definition.Event) {
		ch <- e
		time.Sleep(p)
	}
	// Node names must be unique per scenario because nodes outlive the
	// scenario until the delete events below are processed.
	nodeNames := make([]string, 0, numLargeNode+numSmallNode)
	for i := range numLargeNode {
		nodeNames = append(nodeNames, fmt.Sprintf("node-%s-gpu%d-%d", schedulerName, largeNodeGPU, i))
	}
	for i := range numSmallNode {
		nodeNames = append(nodeNames, fmt.Sprintf("node-%s-gpu%d-%d", schedulerName, smallNodeGPU, i))
	}
	for i, name := range nodeNames {
		gpu := largeNodeGPU
		if i >= numLargeNode {
			gpu = smallNodeGPU
		}
		send(newNodeEvent(name, gpu, interval))
	}
	for range numPreloadJob {
		send(newJobEvent("job-preload-", schedulerName, preloadJobGPU))
	}
	for range numSmallJob {
		send(newJobEvent("job-small-", schedulerName, smallJobGPU))
	}
	for range numMediumJob {
		send(newJobEvent("job-medium-", schedulerName, mediumJobGPU))
	}
	// Clean up nodes after all jobs have finished.
	for i, name := range nodeNames {
		deleteInterval := interval
		if i == 0 {
			deleteInterval = drainInterval
		}
		send(newNodeDeleteEvent(name, deleteInterval))
	}
}

func newNodeDeleteEvent(name string, deleteInterval time.Duration) definition.Event {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
	node.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Node"))
	return definition.NewEvent(definition.EventTypeDelete, node, deleteInterval)
}

func newNodeEvent(name string, gpu int, interval time.Duration) definition.Event {
	resources := corev1.ResourceList{
		corev1.ResourceCPU:         resource.MustParse(fmt.Sprintf("%dm", nodeCPU)),
		corev1.ResourceMemory:      resource.MustParse(fmt.Sprintf("%dGi", nodeMem)),
		corev1.ResourcePods:        resource.MustParse("110"),
		definition.GPUResourceName: resource.MustParse(fmt.Sprintf("%d", gpu)),
	}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Status: corev1.NodeStatus{
			Capacity:    resources,
			Allocatable: resources.DeepCopy(),
		},
	}
	node.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Node")) // NOTE: GVK should be explicitly set for logging
	return definition.NewEvent(definition.EventTypeCreate, node, interval)
}

func newJobEvent(generateName, schedulerName string, gpu int) definition.Event {
	podLabels := map[string]string{"app": "gpu-binpacking-demo"}
	jobResources := corev1.ResourceList{
		corev1.ResourceCPU:         resource.MustParse(fmt.Sprintf("%dm", jobCPU)),
		corev1.ResourceMemory:      resource.MustParse(fmt.Sprintf("%dMi", jobMem)),
		definition.GPUResourceName: resource.MustParse(fmt.Sprintf("%d", gpu)),
	}
	// NOTE: Jobs are immediately marked as Completed by kwok, so use ReplicaSet instead.
	job := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: generateName,
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
							Name:  "gpu-job",
							Image: "registry.example.com/gpu-job:1.0",
							Resources: corev1.ResourceRequirements{
								Requests: jobResources,
								Limits:   jobResources.DeepCopy(),
							},
						},
					},
				},
			},
		},
	}
	job.SetGroupVersionKind(appsv1.SchemeGroupVersion.WithKind("ReplicaSet"))
	return definition.NewEvent(definition.EventTypeCreate, job, interval)
}

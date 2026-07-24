package clusterdata

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pfnet/kube-scheduler-evaluator-reference/scenarios/consts"
	"github.com/pfnet/kube-scheduler-evaluator/pkg/definition"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

const Name = "gpu2023"

const (
	defaultSchedulerName = "default-scheduler"
	nodeListURL          = "https://raw.githubusercontent.com/alibaba/clusterdata/refs/heads/master/cluster-trace-gpu-v2023/csv/openb_node_list_all_node.csv"
	podListURL           = "https://raw.githubusercontent.com/alibaba/clusterdata/refs/heads/master/cluster-trace-gpu-v2023/csv/openb_pod_list_default.csv"
	defaultInterval      = time.Second
	cleanupInterval      = 180 * 24 * time.Hour
)

func Generator(ch chan<- definition.Event) {
	generate(ch, defaultSchedulerName, false)
}

// GenerateForScheduler replays the Alibaba GPU 2023 trace with the requested
// scheduler. When cleanup is true, it removes the trace nodes after every
// workload has completed so another scheduler can replay the same trace.
func GenerateForScheduler(ch chan<- definition.Event, schedulerName string, cleanup bool) {
	generate(ch, schedulerName, cleanup)
}

func generate(ch chan<- definition.Event, schedulerName string, cleanup bool) {
	nodes, err := nodeGenerator(ch)
	if err != nil {
		panic(err)
	}
	if err := podGenerator(ch, schedulerName); err != nil {
		panic(err)
	}
	if cleanup {
		for i, node := range nodes {
			interval := defaultInterval
			if i == 0 {
				interval = cleanupInterval
			}
			ch <- definition.NewEvent(definition.EventTypeDelete, node, interval)
		}
	}
}

// sn,cpu_milli,memory_mib,gpu,model
// openb-node-0229,96000,786432,8,V100M32
func nodeGenerator(ch chan<- definition.Event) ([]*corev1.Node, error) {
	resp, err := http.Get(nodeListURL)
	if err != nil {
		return nil, fmt.Errorf("failed to download node list: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %s while downloading node list", resp.Status)
	}
	reader := csv.NewReader(resp.Body)
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true
	var nodes []*corev1.Node

	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("csv read error: %w", err)
		}
		if len(record) < 5 {
			return nil, fmt.Errorf("clusterdata: malformed record (len=%d)", len(record))
		}

		serverName := strings.TrimSpace(record[0])
		if strings.EqualFold(serverName, "sn") {
			continue
		}
		cpuMilli, err := parseIntField(record[1], "cpu_milli")
		if err != nil {
			return nil, err
		}
		memoryMiB, err := parseIntField(record[2], "memory_mib")
		if err != nil {
			return nil, err
		}
		gpuCount, err := parseIntField(record[3], "gpu")
		if err != nil {
			return nil, err
		}
		model := strings.TrimSpace(record[4])

		node := buildNode(serverName, cpuMilli, memoryMiB, gpuCount, model)
		event := definition.NewEvent(
			definition.EventTypeCreate,
			node,
			defaultInterval,
		)
		ch <- event
		nodes = append(nodes, node)
	}

	return nodes, nil
}

func buildNode(name string, cpuMilli, memoryMiB, gpuCount int64, model string) *corev1.Node {
	capacity := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse(fmt.Sprintf("%dm", cpuMilli)),
		corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%dMi", memoryMiB)),
		corev1.ResourcePods:   resource.MustParse("110"),
	}
	if gpuCount > 0 {
		capacity[corev1.ResourceName("nvidia.com/gpu")] = resource.MustParse(fmt.Sprintf("%d", gpuCount))
	}
	allocatable := make(corev1.ResourceList, len(capacity))
	maps.Copy(allocatable, capacity)

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
			Capacity:    capacity,
			Allocatable: allocatable,
		},
	}
	node.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Node"))
	return node
}

// name,cpu_milli,memory_mib,num_gpu,gpu_milli,gpu_spec,qos,pod_phase,creation_time,deletion_time,scheduled_time
// openb-pod-0035,16000,32768,1,1000,V100M16|V100M32,LS,Running,9967058,9968575,9967063
func podGenerator(ch chan<- definition.Event, schedulerName string) error {
	resp, err := http.Get(podListURL)
	if err != nil {
		return fmt.Errorf("failed to download pod list: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %s while downloading pod list", resp.Status)
	}
	reader := csv.NewReader(resp.Body)
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true

	var (
		previousJob       *appsv1.ReplicaSet
		previousCreation  int64
		havePreviousEvent bool
	)

	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("csv read error: %w", err)
		}
		if len(record) < 11 {
			return fmt.Errorf("clusterdata: malformed record (len=%d)", len(record))
		}

		podName := strings.TrimSpace(record[0])
		if strings.EqualFold(podName, "name") {
			continue
		}
		if podName == "" {
			return fmt.Errorf("clusterdata: record with empty pod name")
		}
		cpuMilli, err := parseIntField(record[1], "cpu_milli")
		if err != nil {
			return err
		}
		memoryMiB, err := parseIntField(record[2], "memory_mib")
		if err != nil {
			return err
		}
		gpuCount, err := parseIntField(record[3], "num_gpu")
		if err != nil {
			return err
		}
		gpuMilli, err := parseIntField(record[4], "gpu_milli")
		if err != nil {
			return err
		}
		gpuSpec := strings.TrimSpace(record[5])
		qos := strings.TrimSpace(record[6])
		creationTime, err := parseIntField(record[8], "creation_time")
		if err != nil {
			return err
		}
		deletionTime, err := parseIntField(record[9], "deletion_time")
		if err != nil {
			return err
		}
		durationSeconds := deletionTime - creationTime
		if durationSeconds < 0 {
			return fmt.Errorf("clusterdata: negative execution duration for %s", podName)
		}
		executionDuration := time.Duration(durationSeconds) * time.Second

		if !havePreviousEvent {
			previousJob = buildJob(
				schedulerName,
				podName,
				cpuMilli,
				memoryMiB,
				gpuCount,
				gpuMilli,
				gpuSpec,
				qos,
				executionDuration,
			)
			previousCreation = creationTime
			havePreviousEvent = true
			continue
		}

		intervalSeconds := creationTime - previousCreation
		if intervalSeconds < 0 {
			return fmt.Errorf("clusterdata: negative interval between pod creations: %d - %d", creationTime, previousCreation)
		}
		interval := time.Duration(intervalSeconds) * time.Second
		if interval <= 0 {
			interval = defaultInterval
		}

		event := definition.NewEvent(
			definition.EventTypeCreate,
			previousJob,
			interval,
		)
		ch <- event

		previousJob = buildJob(
			schedulerName,
			podName,
			cpuMilli,
			memoryMiB,
			gpuCount,
			gpuMilli,
			gpuSpec,
			qos,
			executionDuration,
		)
		previousCreation = creationTime
	}

	if havePreviousEvent {
		event := definition.NewEvent(
			definition.EventTypeCreate,
			previousJob,
			defaultInterval,
		)
		ch <- event
	}

	return nil
}

func buildJob(
	schedulerName, podName string,
	cpuMilli, memoryMiB, gpuCount, gpuMilli int64,
	gpuSpec, qos string,
	executionDuration time.Duration,
) *appsv1.ReplicaSet {
	podLabels := map[string]string{"app": "example-app"}
	deadline := executionDuration * 2
	gpuMilliAnnotationKey := fmt.Sprintf("%s/gpu-milli", consts.UserID)
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
						gpuMilliAnnotationKey: fmt.Sprintf("%d", gpuMilli),
						gpuSpecAnnotationKey:  gpuSpec,
						qosAnnotationKey:      qos,
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
									"nvidia.com/gpu":      resource.MustParse(fmt.Sprintf("%d", gpuCount)),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse(fmt.Sprintf("%dm", cpuMilli)),
									corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%dMi", memoryMiB)),
									"nvidia.com/gpu":      resource.MustParse(fmt.Sprintf("%d", gpuCount)),
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

func parseIntField(raw, field string) (int64, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, fmt.Errorf("%s field is empty", field)
	}
	value, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid %s value %q: %w", field, raw, err)
	}
	return value, nil
}

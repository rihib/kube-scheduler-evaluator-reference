package clusterdata

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pfnet/kube-scheduler-evaluator-reference/scenarios/consts"
	"github.com/pfnet/kube-scheduler-evaluator/pkg/definition"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

const Name = "gpu2023"

const (
	schedulerName   = "default-scheduler"
	nodeListURL     = "https://raw.githubusercontent.com/alibaba/clusterdata/refs/heads/master/cluster-trace-gpu-v2023/csv/openb_node_list_all_node.csv"
	podListURL      = "https://raw.githubusercontent.com/alibaba/clusterdata/refs/heads/master/cluster-trace-gpu-v2023/csv/openb_pod_list_default.csv"
	defaultInterval = time.Second

	// nodeDeletionDelay is the interval between the last pod creation and the node deletions.
	// It must be long enough for every pod to finish, because the nodes are deleted
	// so that the next iteration or scenario starts from an empty cluster.
	// If a pod is still running or pending when the nodes are deleted,
	// the pending pods cannot be scheduled and the evaluator fails at the finish event.
	nodeDeletionDelay = 100 * 365 * 24 * time.Hour
)

func Generator(ch chan<- definition.Event) {
	nodes, err := nodeGenerator(ch)
	if err != nil {
		panic(err)
	}
	if err := podGenerator(ch, schedulerName); err != nil {
		panic(err)
	}
	nodeDeleter(ch, nodes)
}

// nodeDeleter deletes all nodes after all pods have finished.
func nodeDeleter(ch chan<- definition.Event, nodes []*corev1.Node) {
	for i, node := range nodes {
		interval := time.Duration(0)
		if i == 0 {
			interval = nodeDeletionDelay
		}
		ch <- definition.NewEvent(
			definition.EventTypeDelete,
			node,
			interval,
		)
	}
}

func nodeGenerator(ch chan<- definition.Event) ([]*corev1.Node, error) {
	resp, err := http.Get(nodeListURL)
	if err != nil {
		return nil, fmt.Errorf("failed to download node list: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %s while downloading node list", resp.Status)
	}

	nodes, events, err := parseNodeRecords(resp.Body)
	if err != nil {
		return nil, err
	}
	for _, event := range events {
		ch <- event
	}

	return nodes, nil
}

// buildNode builds a Node whose GPUs (if any) are the nvidia.com/gpu
// extended resource, the representation gpu2023.go's full openb pipeline
// uses. It shares buildNodeBase/finalizeNodeCapacity with
// buildInputVersionNode (input_version.go), which instead represents GPUs
// with the alibabacloud.com/* capacity keys (decisions.md's "GPU の
// annotation の形"); everything else about a Node (CPU/memory capacity, the
// model label, ObjectMeta, GVK) is common to both and lives in those two
// helpers.
func buildNode(name string, cpuMilli, memoryMiB, gpuCount int64, model string) *corev1.Node {
	node := buildNodeBase(name, cpuMilli, memoryMiB, model)
	if gpuCount > 0 {
		node.Status.Capacity[corev1.ResourceName("nvidia.com/gpu")] = resource.MustParse(fmt.Sprintf("%d", gpuCount))
	}
	finalizeNodeCapacity(node)
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

// buildJob builds the ReplicaSet for a pod whose GPU request (if any) is
// the nvidia.com/gpu extended resource plus gpu2023's own gpu-milli
// annotation, the representation gpu2023.go's full openb pipeline uses. It
// shares buildJobBase with buildInputVersionJob (input_version.go), which
// instead represents GPUs with the alibabacloud.com/* annotations
// (decisions.md's "GPU の annotation の形"); everything else about the Job
// (ObjectMeta, Selector, deadline/execution-duration/qos/gpu-spec
// annotations, the container's cpu/memory Requests/Limits) is common to
// both and lives in that helper.
func buildJob(
	schedulerName, podName string,
	cpuMilli, memoryMiB, gpuCount, gpuMilli int64,
	gpuSpec, qos string,
	executionDuration time.Duration,
) *appsv1.ReplicaSet {
	job := buildJobBase(schedulerName, podName, cpuMilli, memoryMiB, gpuSpec, qos, executionDuration)

	gpuMilliAnnotationKey := fmt.Sprintf("%s/gpu-milli", consts.UserID)
	job.Spec.Template.Annotations[gpuMilliAnnotationKey] = fmt.Sprintf("%d", gpuMilli)

	gpuQuantity := resource.MustParse(fmt.Sprintf("%d", gpuCount))
	job.Spec.Template.Spec.Containers[0].Resources.Requests["nvidia.com/gpu"] = gpuQuantity
	job.Spec.Template.Spec.Containers[0].Resources.Limits["nvidia.com/gpu"] = gpuQuantity

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

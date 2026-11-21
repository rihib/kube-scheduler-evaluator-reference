package clusterdata

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"sort"
	"strings"
	"time"

	"github.com/pfnet/kube-scheduler-evaluator-reference/scenarios/consts"
	"github.com/pfnet/kube-scheduler-evaluator/pkg/definition"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

// InputVersionSummary describes how BuildInputVersion turned the openb trace
// into one input version: how many pods ended up in it, how many were left
// out, how many of the used pods were Failed, and the load factor (rho)
// before and after the interval scaling that targets targetRho.
type InputVersionSummary struct {
	UsedPodCount     int
	ExcludedPodCount int
	FailedPodCount   int
	RhoBeforeScaling float64
	RhoAfterScaling  float64
}

// BuildInputVersion reads the openb node and pod CSVs and produces one input
// version: a sequence of events (node creation, pod creation, and the final
// node deletion) together with a summary of how the version was built.
//
// seed selects which sampleRatio of the non-Pending pods are kept and how
// pods sharing the same creation_time are ordered. targetRho is the load
// factor the submission intervals are scaled to reach.
func BuildInputVersion(
	nodesCSV, podsCSV io.Reader,
	seed int64,
	targetRho, sampleRatio float64,
) ([]definition.Event, InputVersionSummary, error) {
	if sampleRatio < 0 || sampleRatio > 1 {
		return nil, InputVersionSummary{}, fmt.Errorf("clusterdata: sampleRatio must be between 0 and 1, got %v", sampleRatio)
	}
	if targetRho <= 0 {
		return nil, InputVersionSummary{}, fmt.Errorf("clusterdata: targetRho must be positive, got %v", targetRho)
	}

	nodes, nodeEvents, err := parseInputVersionNodes(nodesCSV)
	if err != nil {
		return nil, InputVersionSummary{}, err
	}

	allPods, err := parseInputVersionPods(podsCSV)
	if err != nil {
		return nil, InputVersionSummary{}, err
	}

	var eligible []openbPodRecord
	for _, p := range allPods {
		if strings.EqualFold(p.phase, "Pending") {
			continue
		}
		eligible = append(eligible, p)
	}

	rng := rand.New(rand.NewSource(seed))
	used := sampleAndOrderPods(eligible, sampleRatio, rng)

	failedCount := 0
	for _, p := range used {
		if strings.EqualFold(p.phase, "Failed") {
			failedCount++
		}
	}

	capacity := nodeCapacityTotals(nodes)
	demand := podDemandTotals(used)
	periodBeforeScaling := submissionPeriodSeconds(used)
	rhoBeforeScaling := rho(demand, capacity, periodBeforeScaling)

	multiplier := rhoBeforeScaling / targetRho

	events := make([]definition.Event, 0, len(nodeEvents)+len(used)+len(nodes))
	events = append(events, nodeEvents...)
	events = append(events, buildInputVersionPodEvents(used, multiplier)...)
	events = append(events, inputVersionNodeDeleteEvents(nodes)...)

	rhoAfterScaling := rho(demand, capacity, periodBeforeScaling*multiplier)

	summary := InputVersionSummary{
		UsedPodCount:     len(used),
		ExcludedPodCount: len(allPods) - len(used),
		FailedPodCount:   failedCount,
		RhoBeforeScaling: rhoBeforeScaling,
		RhoAfterScaling:  rhoAfterScaling,
	}

	return events, summary, nil
}

// sampleAndOrderPods draws sampleRatio of eligible using rng, then orders
// the result by creation_time. rng.Perm puts the drawn pods in a random
// order first, and the stable sort by creation_time keeps that random
// order among pods that share the same creation_time, which is how their
// submission order gets shuffled.
func sampleAndOrderPods(eligible []openbPodRecord, sampleRatio float64, rng *rand.Rand) []openbPodRecord {
	perm := rng.Perm(len(eligible))
	usedCount := int(float64(len(eligible))*sampleRatio + 0.5)

	used := make([]openbPodRecord, 0, usedCount)
	for _, i := range perm[:usedCount] {
		used = append(used, eligible[i])
	}

	sort.SliceStable(used, func(i, j int) bool {
		return used[i].creationTime < used[j].creationTime
	})

	return used
}

// parseOptionalIntField parses field like parseIntField, except an empty
// value parses as 0 instead of failing.
func parseOptionalIntField(raw, field string) (int64, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	return parseIntField(raw, field)
}

// resourceTotals holds a quantity (a demand or a capacity) for each of the
// three resources rho is computed over.
type resourceTotals struct {
	cpuMilli  float64
	memoryMiB float64
	gpu       float64
}

// podDemandTotals sums, for each resource, the request times the execution
// duration (in seconds) over pods.
func podDemandTotals(pods []openbPodRecord) resourceTotals {
	var t resourceTotals
	for _, p := range pods {
		durationSeconds := float64(p.deletionTime - p.scheduledTime)
		t.cpuMilli += float64(p.cpuMilli) * durationSeconds
		t.memoryMiB += float64(p.memoryMiB) * durationSeconds
		t.gpu += float64(p.gpuCount) * durationSeconds
	}
	return t
}

// nodeCapacityTotals sums the capacity of every node for each resource.
func nodeCapacityTotals(nodes []*corev1.Node) resourceTotals {
	var t resourceTotals
	for _, n := range nodes {
		t.cpuMilli += float64(n.Status.Capacity.Cpu().MilliValue())
		t.memoryMiB += float64(n.Status.Capacity.Memory().Value()) / (1024 * 1024)
		if gpu, ok := n.Status.Capacity[corev1.ResourceName("nvidia.com/gpu")]; ok {
			t.gpu += float64(gpu.Value())
		}
	}
	return t
}

// submissionPeriodSeconds is the observation period of pods ordered by
// creation_time: from the first submission to the last.
func submissionPeriodSeconds(pods []openbPodRecord) float64 {
	if len(pods) == 0 {
		return 0
	}
	return float64(pods[len(pods)-1].creationTime - pods[0].creationTime)
}

// rho is the load factor: for each resource with nonzero capacity, demand
// divided by (capacity * periodSeconds), and the maximum across resources.
func rho(demand, capacity resourceTotals, periodSeconds float64) float64 {
	if periodSeconds <= 0 {
		return 0
	}
	max := 0.0
	for _, r := range []struct{ demand, capacity float64 }{
		{demand.cpuMilli, capacity.cpuMilli},
		{demand.memoryMiB, capacity.memoryMiB},
		{demand.gpu, capacity.gpu},
	} {
		if r.capacity <= 0 {
			continue
		}
		ratio := r.demand / (r.capacity * periodSeconds)
		if ratio > max {
			max = ratio
		}
	}
	return max
}

// buildInputVersionPodEvents builds the pod Create events in submission
// order. evaluator adds an event's interval to the clock before that event
// fires (internal/scenario/manager.go: nextRelativeTime += d.Interval()), so
// an event's interval is its own wait, not the wait until the next one. The
// first pod therefore gets interval 0, and every later pod gets the gap to
// its predecessor's creation_time, scaled by multiplier so the resulting
// rho matches targetRho.
func buildInputVersionPodEvents(pods []openbPodRecord, multiplier float64) []definition.Event {
	events := make([]definition.Event, 0, len(pods))
	for i, p := range pods {
		interval := time.Duration(0)
		if i > 0 {
			gapSeconds := float64(p.creationTime - pods[i-1].creationTime)
			interval = time.Duration(gapSeconds * multiplier * float64(time.Second))
		}
		events = append(events, definition.NewEvent(definition.EventTypeCreate, buildInputVersionJob(p), interval))
	}
	return events
}

// openbPodRecord is one row of the openb pod CSV, parsed into typed fields.
type openbPodRecord struct {
	name          string
	cpuMilli      int64
	memoryMiB     int64
	gpuCount      int64
	gpuSpec       string
	qos           string
	phase         string
	creationTime  int64
	deletionTime  int64
	scheduledTime int64
}

// name,cpu_milli,memory_mib,num_gpu,gpu_milli,gpu_spec,qos,pod_phase,creation_time,deletion_time,scheduled_time
func parseInputVersionPods(podsCSV io.Reader) ([]openbPodRecord, error) {
	reader := csv.NewReader(podsCSV)
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true

	var records []openbPodRecord
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("clusterdata: pod csv read error: %w", err)
		}
		if len(record) < 11 {
			return nil, fmt.Errorf("clusterdata: malformed pod record (len=%d)", len(record))
		}

		name := strings.TrimSpace(record[0])
		if strings.EqualFold(name, "name") {
			continue
		}
		if name == "" {
			return nil, fmt.Errorf("clusterdata: pod record with empty name")
		}

		cpuMilli, err := parseIntField(record[1], "cpu_milli")
		if err != nil {
			return nil, err
		}
		memoryMiB, err := parseIntField(record[2], "memory_mib")
		if err != nil {
			return nil, err
		}
		gpuCount, err := parseIntField(record[3], "num_gpu")
		if err != nil {
			return nil, err
		}
		gpuSpec := strings.TrimSpace(record[5])
		qos := strings.TrimSpace(record[6])
		phase := strings.TrimSpace(record[7])
		creationTime, err := parseIntField(record[8], "creation_time")
		if err != nil {
			return nil, err
		}
		deletionTime, err := parseIntField(record[9], "deletion_time")
		if err != nil {
			return nil, err
		}
		// scheduled_time is empty for Pending pods in the openb trace (they
		// were never scheduled). BuildInputVersion excludes Pending pods, so
		// this value is unused for them; parse it as 0 instead of failing.
		scheduledTime, err := parseOptionalIntField(record[10], "scheduled_time")
		if err != nil {
			return nil, err
		}
		if deletionTime < scheduledTime {
			return nil, fmt.Errorf("clusterdata: negative execution duration for %s", name)
		}

		records = append(records, openbPodRecord{
			name:          name,
			cpuMilli:      cpuMilli,
			memoryMiB:     memoryMiB,
			gpuCount:      gpuCount,
			gpuSpec:       gpuSpec,
			qos:           qos,
			phase:         phase,
			creationTime:  creationTime,
			deletionTime:  deletionTime,
			scheduledTime: scheduledTime,
		})
	}

	return records, nil
}

// parseInputVersionNodes reads the openb node CSV and builds the Create
// events for every node, since an input version uses all of them.
func parseInputVersionNodes(nodesCSV io.Reader) ([]*corev1.Node, []definition.Event, error) {
	reader := csv.NewReader(nodesCSV)
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true

	var (
		nodes  []*corev1.Node
		events []definition.Event
	)
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("clusterdata: node csv read error: %w", err)
		}
		if len(record) < 5 {
			return nil, nil, fmt.Errorf("clusterdata: malformed node record (len=%d)", len(record))
		}

		serverName := strings.TrimSpace(record[0])
		if strings.EqualFold(serverName, "sn") {
			continue
		}

		cpuMilli, err := parseIntField(record[1], "cpu_milli")
		if err != nil {
			return nil, nil, err
		}
		memoryMiB, err := parseIntField(record[2], "memory_mib")
		if err != nil {
			return nil, nil, err
		}
		gpuCount, err := parseIntField(record[3], "gpu")
		if err != nil {
			return nil, nil, err
		}
		model := strings.TrimSpace(record[4])

		node := buildNode(serverName, cpuMilli, memoryMiB, gpuCount, model)
		nodes = append(nodes, node)
		events = append(events, definition.NewEvent(definition.EventTypeCreate, node, defaultInterval))
	}

	return nodes, events, nil
}

// inputVersionNodeDeleteEvents deletes all nodes after all pods have
// finished, the same way nodeDeleter does for the full openb scenario.
func inputVersionNodeDeleteEvents(nodes []*corev1.Node) []definition.Event {
	events := make([]definition.Event, 0, len(nodes))
	for i, node := range nodes {
		interval := time.Duration(0)
		if i == 0 {
			interval = nodeDeletionDelay
		}
		events = append(events, definition.NewEvent(definition.EventTypeDelete, node, interval))
	}
	return events
}

// gpuMilliPerGPU is the gpu-milli annotation evaluator expects per
// requested GPU. evaluator prefers this annotation over its own default
// (also 1000 milli per GPU, see requestedResources in
// internal/scenario/manager.go) when computing how much GPU a pod
// requested, so passing the default explicitly keeps the recorded GPU
// utilization correct without depending on the CSV's unused gpu_milli
// field (BuildInputVersion uses num_gpu for the GPU request, not gpu_milli).
const gpuMilliPerGPU = 1000

func buildInputVersionJob(p openbPodRecord) *appsv1.ReplicaSet {
	job := buildJob(
		"default-scheduler",
		p.name,
		p.cpuMilli,
		p.memoryMiB,
		p.gpuCount,
		gpuMilliPerGPU,
		p.gpuSpec,
		p.qos,
		time.Duration(p.deletionTime-p.scheduledTime)*time.Second,
	)
	if affinity := gpuSpecNodeAffinity(p.gpuSpec); affinity != nil {
		job.Spec.Template.Spec.Affinity = affinity
	}
	return job
}

// gpuSpecNodeAffinity turns a non-empty gpu_spec field (a "|"-separated list
// of GPU model names, e.g. "V100M16|V100M32") into a nodeAffinity that only
// allows scheduling onto nodes whose model label matches one of them. An
// empty gpu_spec means no constraint, so it returns nil.
func gpuSpecNodeAffinity(gpuSpec string) *corev1.Affinity {
	if gpuSpec == "" {
		return nil
	}
	modelLabelKey := fmt.Sprintf("%s/model", consts.UserID)
	return &corev1.Affinity{
		NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{
					{
						MatchExpressions: []corev1.NodeSelectorRequirement{
							{
								Key:      modelLabelKey,
								Operator: corev1.NodeSelectorOpIn,
								Values:   strings.Split(gpuSpec, "|"),
							},
						},
					},
				},
			},
		},
	}
}

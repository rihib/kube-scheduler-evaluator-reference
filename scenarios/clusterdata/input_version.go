package clusterdata

import (
	"fmt"
	"io"
	"math"
	"math/rand"
	"sort"
	"strings"
	"time"

	"github.com/pfnet/kube-scheduler-evaluator-reference/scenarios/consts"
	"github.com/pfnet/kube-scheduler-evaluator/pkg/definition"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// alibabacloudGPUMilliKey and alibabacloudGPUCountKey are the pod
// annotations and node capacity/allocatable keys FGD's public
// implementation (hkust-adsl/kubernetes-scheduler-simulator,
// pkg/type/open-gpu-share/utils/const.go) uses to represent GPU requests and
// capacity that are not whole GPUs: a GPU-milli share (1000 == 1 GPU) and a
// GPU count. decisions.md's "GPU の annotation の形" adopts the same keys so
// the FGD plugin (a later unit) can read input versions built here without
// any translation.
const (
	alibabacloudGPUMilliKey = "alibabacloud.com/gpu-milli"
	alibabacloudGPUCountKey = "alibabacloud.com/gpu-count"
)

// nodeShapeKey groups openb nodes the way decisions.md's node-shrinking
// rule does: by GPU count, GPU model, CPU, and memory. Nodes with the same
// GPU count and model still differ in CPU/memory in openb (three CPU/memory
// combinations share 2x P100, for instance; spec.md's decision 8), so the
// shape has to include CPU and memory too, or two differently-sized node
// types would be shrunk as if they were interchangeable.
type nodeShapeKey struct {
	gpuCount  int64
	model     string
	cpuMilli  int64
	memoryMiB int64
}

// NodeShapeSummary is how many openb nodes of one shape (GPU count, GPU
// model, CPU, memory) ended up in the shrunk cluster BuildInputVersion
// built.
type NodeShapeSummary struct {
	GPUCount  int64
	Model     string
	CPUMilli  int64
	MemoryMiB int64
	Count     int
}

// ResourceRho is the load factor (rho, spec.md's decision 7 / glossary
// definition A) for each of the three resources rho is computed over, and
// their maximum, which is rho itself.
type ResourceRho struct {
	CPUMilli  float64
	MemoryMiB float64
	GPU       float64
}

// Max is rho itself: the maximum of the three resources' ratios.
func (r ResourceRho) Max() float64 {
	return max(r.CPUMilli, r.MemoryMiB, r.GPU)
}

// InputVersionSummary describes how BuildInputVersion turned the openb
// trace into one input version: which nodes it kept out of the shrunk
// cluster, how many pods ended up in it (and how many were excluded or
// Failed), and the load factor (rho) before and after the interval scaling
// that targets targetRho.
type InputVersionSummary struct {
	SelectedNodeCount       int
	NodeShapes              []NodeShapeSummary
	UsedPodCount            int
	ExcludedPendingPodCount int
	FailedPodCount          int
	Multiplier              float64
	// SubmissionPeriod is the input version's actual submission period
	// (after Multiplier has scaled it), not the openb trace's original
	// period.
	SubmissionPeriod time.Duration
	RhoBeforeScaling ResourceRho
	RhoAfterScaling  ResourceRho
}

// BuildInputVersion reads the openb node and pod CSVs and produces one
// input version: a sequence of events (node creation, pod creation, and the
// final node deletion) together with a summary of how the version was
// built.
//
// seed selects which nodes of each shape are kept when the cluster is
// shrunk by nodeShrinkRatio, and how pods sharing the same creation_time are
// ordered. targetRho is the load factor the submission intervals are scaled
// to reach.
func BuildInputVersion(
	nodesCSV, podsCSV io.Reader,
	seed int64,
	targetRho, nodeShrinkRatio float64,
) ([]definition.Event, InputVersionSummary, error) {
	if math.IsNaN(nodeShrinkRatio) || nodeShrinkRatio < 0 || nodeShrinkRatio > 1 {
		return nil, InputVersionSummary{}, fmt.Errorf("clusterdata: nodeShrinkRatio must be between 0 and 1, got %v", nodeShrinkRatio)
	}
	if math.IsNaN(targetRho) || targetRho <= 0 {
		return nil, InputVersionSummary{}, fmt.Errorf("clusterdata: targetRho must be positive, got %v", targetRho)
	}

	nodeRecords, err := parseNodeCSVRecords(nodesCSV)
	if err != nil {
		return nil, InputVersionSummary{}, err
	}

	allPods, err := parseInputVersionPods(podsCSV)
	if err != nil {
		return nil, InputVersionSummary{}, err
	}

	rng := rand.New(rand.NewSource(seed))

	selectedNodes, shapes := shrinkNodesByShape(nodeRecords, nodeShrinkRatio, rng)

	var eligible []openbPodRecord
	for _, p := range allPods {
		if strings.EqualFold(p.phase, "Pending") {
			continue
		}
		eligible = append(eligible, p)
	}
	used := orderPods(eligible, rng)

	if err := checkPodsFitSomeNode(used, selectedNodes); err != nil {
		return nil, InputVersionSummary{}, err
	}

	failedCount := 0
	for _, p := range used {
		if strings.EqualFold(p.phase, "Failed") {
			failedCount++
		}
	}

	capacity := nodeCapacityTotals(selectedNodes)
	periodStart, periodEnd := submissionPeriodBounds(used)
	periodBeforeScaling := float64(periodEnd - periodStart)
	if len(used) > 0 && periodBeforeScaling <= 0 {
		return nil, InputVersionSummary{}, fmt.Errorf("clusterdata: submission period is zero; every used pod shares the same creation_time, so rho cannot be computed")
	}
	if len(used) > 0 && capacity.cpuMilli <= 0 && capacity.memoryMiB <= 0 && capacity.gpu <= 0 {
		return nil, InputVersionSummary{}, fmt.Errorf("clusterdata: no node has any capacity; rho cannot be computed")
	}

	var multiplier float64
	var rhoBeforeScaling, rhoAfterScaling ResourceRho
	if len(used) > 0 {
		demandBeforeScaling := demandAtMultiplier(used, periodStart, periodBeforeScaling, 1.0)
		rhoBeforeScaling = rhoPerResource(demandBeforeScaling, capacity, periodBeforeScaling)
		if rhoBeforeScaling.Max() <= 0 {
			return nil, InputVersionSummary{}, fmt.Errorf("clusterdata: rho is zero; the used pods request nothing or overlap the submission period for no time, so the submission intervals cannot be scaled to the target rho")
		}

		multiplier, err = findMultiplier(used, capacity, periodStart, periodBeforeScaling, targetRho)
		if err != nil {
			return nil, InputVersionSummary{}, err
		}
		demandAfterScaling := demandAtMultiplier(used, periodStart, periodBeforeScaling, multiplier)
		rhoAfterScaling = rhoPerResource(demandAfterScaling, capacity, periodBeforeScaling*multiplier)
	}

	selectedCoreNodes := make([]*corev1.Node, 0, len(selectedNodes))
	for _, n := range selectedNodes {
		selectedCoreNodes = append(selectedCoreNodes, buildInputVersionNode(n))
	}

	events := make([]definition.Event, 0, len(selectedCoreNodes)*2+len(used))
	events = append(events, inputVersionNodeCreateEvents(selectedCoreNodes)...)
	events = append(events, buildInputVersionPodEvents(used, multiplier)...)
	events = append(events, inputVersionNodeDeleteEvents(selectedCoreNodes)...)

	summary := InputVersionSummary{
		SelectedNodeCount:       len(selectedNodes),
		NodeShapes:              shapes,
		UsedPodCount:            len(used),
		ExcludedPendingPodCount: len(allPods) - len(eligible),
		FailedPodCount:          failedCount,
		Multiplier:              multiplier,
		// Multiplying by multiplier before converting to a Duration (rather
		// than converting periodBeforeScaling to a Duration first and then
		// multiplying) keeps sub-second precision: time.Duration(x) floors
		// x, so flooring whole seconds first can lose up to a full second
		// once multiplier makes the scaled period fractional, the same
		// reason buildInputVersionPodEvents scales before converting.
		SubmissionPeriod: time.Duration(periodBeforeScaling * multiplier * float64(time.Second)),
		RhoBeforeScaling: rhoBeforeScaling,
		RhoAfterScaling:  rhoAfterScaling,
	}

	return events, summary, nil
}

// shrinkNodesByShape groups nodes by shape (nodeShapeKey) and keeps
// round(len(shape)*nodeShrinkRatio) of each shape, chosen by rng, with
// every shape keeping at least one node (spec.md's decision 8: どの形も、
// 最低1台は残す). Shapes are visited in a fixed order (sorted by key) so
// that, for a given seed, which nodes are kept does not depend on the CSV's
// row order.
func shrinkNodesByShape(nodes []openbNodeRecord, nodeShrinkRatio float64, rng *rand.Rand) ([]openbNodeRecord, []NodeShapeSummary) {
	byShape := map[nodeShapeKey][]openbNodeRecord{}
	for _, n := range nodes {
		key := nodeShapeKey{gpuCount: n.gpuCount, model: n.model, cpuMilli: n.cpuMilli, memoryMiB: n.memoryMiB}
		byShape[key] = append(byShape[key], n)
	}

	keys := make([]nodeShapeKey, 0, len(byShape))
	for k := range byShape {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.gpuCount != b.gpuCount {
			return a.gpuCount < b.gpuCount
		}
		if a.model != b.model {
			return a.model < b.model
		}
		if a.cpuMilli != b.cpuMilli {
			return a.cpuMilli < b.cpuMilli
		}
		return a.memoryMiB < b.memoryMiB
	})

	var selected []openbNodeRecord
	shapes := make([]NodeShapeSummary, 0, len(keys))
	for _, key := range keys {
		shapeNodes := byShape[key]
		keepCount := int(math.Round(float64(len(shapeNodes)) * nodeShrinkRatio))
		if keepCount < 1 {
			keepCount = 1
		}
		if keepCount > len(shapeNodes) {
			keepCount = len(shapeNodes)
		}

		perm := rng.Perm(len(shapeNodes))
		for _, i := range perm[:keepCount] {
			selected = append(selected, shapeNodes[i])
		}

		shapes = append(shapes, NodeShapeSummary{
			GPUCount:  key.gpuCount,
			Model:     key.model,
			CPUMilli:  key.cpuMilli,
			MemoryMiB: key.memoryMiB,
			Count:     keepCount,
		})
	}

	return selected, shapes
}

// orderPods shuffles pods into a random order with rng, then stably sorts
// them by creation_time. rng.Perm puts them in a random order first, and
// the stable sort by creation_time keeps that random order among pods that
// share the same creation_time, which is how their submission order gets
// shuffled.
func orderPods(pods []openbPodRecord, rng *rand.Rand) []openbPodRecord {
	perm := rng.Perm(len(pods))
	ordered := make([]openbPodRecord, len(pods))
	for i, from := range perm {
		ordered[i] = pods[from]
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].creationTime < ordered[j].creationTime
	})
	return ordered
}

// checkPodsFitSomeNode returns an error naming the first pod whose request
// (CPU, memory, GPU count) exceeds every node in nodes. units/U02.md's
// Implementation Decisions treats this as a defensive check: with every
// shape keeping at least one node (spec.md's decision 8), it should not
// normally trigger, but if it did go unchecked, the
// pod would stay pending for the rest of the run and evaluator would fail
// at the scenario's finish event instead of at input-version build time.
func checkPodsFitSomeNode(pods []openbPodRecord, nodes []openbNodeRecord) error {
	for _, p := range pods {
		fits := false
		for _, n := range nodes {
			if p.cpuMilli <= n.cpuMilli && p.memoryMiB <= n.memoryMiB && p.gpuCount <= n.gpuCount {
				fits = true
				break
			}
		}
		if !fits {
			return fmt.Errorf("clusterdata: pod %s requests more than any node in the shrunk cluster provides (cpu=%dm, memory=%dMi, gpu=%d)", p.name, p.cpuMilli, p.memoryMiB, p.gpuCount)
		}
	}
	return nil
}

// resourceTotals holds a quantity (a demand or a capacity) for each of the
// three resources rho is computed over. gpu is in units of whole GPUs
// (fractional for demand, since spec.md's decision 7 uses the GPU share
// request uncapped; whole-number for node capacity, since a node has an
// integer GPU count).
type resourceTotals struct {
	cpuMilli  float64
	memoryMiB float64
	gpu       float64
}

// submissionPeriodBounds is the earliest and latest creation_time among
// pods, the observation window rho's definition A measures each pod's
// overlap against.
func submissionPeriodBounds(pods []openbPodRecord) (start, end int64) {
	if len(pods) == 0 {
		return 0, 0
	}
	start, end = pods[0].creationTime, pods[0].creationTime
	for _, p := range pods[1:] {
		if p.creationTime < start {
			start = p.creationTime
		}
		if p.creationTime > end {
			end = p.creationTime
		}
	}
	return start, end
}

// demandAtMultiplier sums, for each resource, the request times the
// overlap between a pod's run (from its creation_time, scaled by
// multiplier the same way buildInputVersionPodEvents scales submission
// gaps, to creation_time+duration) and the submission period [periodStart,
// periodStart+periodBeforeScaling*multiplier) (glossary's rho definition
// A). A pod's distance from periodStart (its creation_time minus
// periodStart) scales by multiplier the same way its submission gap to the
// previous pod does, so its scaled position is periodStart +
// (creation_time-periodStart)*multiplier; the period's end scales the same
// way. Writing gap = periodBeforeScaling - (creation_time-periodStart) (how
// far, in unscaled seconds, the pod's creation_time is from the
// unscaled period's end), the scaled overlap is gap*multiplier, clamped to
// [0, duration].
func demandAtMultiplier(pods []openbPodRecord, periodStart int64, periodBeforeScaling, multiplier float64) resourceTotals {
	var t resourceTotals
	for _, p := range pods {
		gap := periodBeforeScaling - float64(p.creationTime-periodStart)
		duration := float64(p.deletionTime - p.scheduledTime)

		overlap := gap * multiplier
		if overlap < 0 {
			overlap = 0
		}
		if overlap > duration {
			overlap = duration
		}

		effectiveGPU := float64(p.gpuCount) * float64(p.gpuMilli) / 1000.0

		t.cpuMilli += float64(p.cpuMilli) * overlap
		t.memoryMiB += float64(p.memoryMiB) * overlap
		t.gpu += effectiveGPU * overlap
	}
	return t
}

// nodeCapacityTotals sums the capacity of every node for each resource.
func nodeCapacityTotals(nodes []openbNodeRecord) resourceTotals {
	var t resourceTotals
	for _, n := range nodes {
		t.cpuMilli += float64(n.cpuMilli)
		t.memoryMiB += float64(n.memoryMiB)
		t.gpu += float64(n.gpuCount)
	}
	return t
}

// rhoPerResource is each resource's ratio of demand to (capacity *
// periodSeconds): the load factor rho is computed over, before taking
// their maximum. A resource with no capacity across any node is left at 0
// rather than dividing by zero, the same way a resource nobody uses
// contributes 0 demand.
func rhoPerResource(demand, capacity resourceTotals, periodSeconds float64) ResourceRho {
	if periodSeconds <= 0 {
		return ResourceRho{}
	}
	var r ResourceRho
	if capacity.cpuMilli > 0 {
		r.CPUMilli = demand.cpuMilli / (capacity.cpuMilli * periodSeconds)
	}
	if capacity.memoryMiB > 0 {
		r.MemoryMiB = demand.memoryMiB / (capacity.memoryMiB * periodSeconds)
	}
	if capacity.gpu > 0 {
		r.GPU = demand.gpu / (capacity.gpu * periodSeconds)
	}
	return r
}

// rhoAtMultiplier is rho (the maximum of the three resources' ratios) after
// scaling submission gaps by multiplier.
func rhoAtMultiplier(pods []openbPodRecord, capacity resourceTotals, periodStart int64, periodBeforeScaling, multiplier float64) float64 {
	periodSeconds := periodBeforeScaling * multiplier
	if periodSeconds <= 0 {
		return 0
	}
	demand := demandAtMultiplier(pods, periodStart, periodBeforeScaling, multiplier)
	return rhoPerResource(demand, capacity, periodSeconds).Max()
}

// findMultiplier binary-searches for the multiplier that scales submission
// gaps so that rhoAtMultiplier equals targetRho. Definition A's overlap
// clamps a pod's demand contribution to its duration, so (unlike a formula
// without that clamp) rho is not simply proportional to 1/multiplier:
// shrinking the gaps compresses the period, but it cannot raise a pod's
// contribution past its own duration, so rho approaches a ceiling as
// multiplier shrinks towards 0 instead of growing without bound. This is
// the same ceiling spec.md's decision 7 describes for the 1213-node
// cluster's GPU rho (it caps at 0.094 no matter how far submission
// intervals are compressed); shrinking the cluster (decision 8) is what
// raises the ceiling enough to reach targetRho.
func findMultiplier(pods []openbPodRecord, capacity resourceTotals, periodStart int64, periodBeforeScaling, targetRho float64) (float64, error) {
	const (
		lowMultiplier = 1e-9
		maxHigh       = 1e18
		iterations    = 200
	)

	rhoAtLow := rhoAtMultiplier(pods, capacity, periodStart, periodBeforeScaling, lowMultiplier)
	if rhoAtLow < targetRho {
		return 0, fmt.Errorf("clusterdata: target rho %v is unreachable; even at the shortest reachable submission period, rho is only %v", targetRho, rhoAtLow)
	}

	low, high := lowMultiplier, 1.0
	for rhoAtMultiplier(pods, capacity, periodStart, periodBeforeScaling, high) > targetRho {
		high *= 2
		if high > maxHigh {
			return 0, fmt.Errorf("clusterdata: could not find a multiplier reaching target rho %v", targetRho)
		}
	}

	for i := 0; i < iterations; i++ {
		mid := (low + high) / 2
		if rhoAtMultiplier(pods, capacity, periodStart, periodBeforeScaling, mid) > targetRho {
			low = mid
		} else {
			high = mid
		}
	}

	return (low + high) / 2, nil
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

// inputVersionNodeCreateEvents creates every node at the start of the input
// version, the same way parseNodeRecords does for the full openb scenario.
func inputVersionNodeCreateEvents(nodes []*corev1.Node) []definition.Event {
	events := make([]definition.Event, 0, len(nodes))
	for _, node := range nodes {
		events = append(events, definition.NewEvent(definition.EventTypeCreate, node, defaultInterval))
	}
	return events
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

// buildInputVersionNode builds the corev1.Node for one selected openb node,
// representing its GPUs the way FGD's public implementation does
// (decisions.md's "GPU の annotation の形"): a GPU count and a GPU-milli
// capacity (gpuCount*1000, a whole node's GPUs are never shared), both as
// capacity/allocatable resources under the alibabacloud.com keys, instead
// of the nvidia.com/gpu extended resource buildNode (gpu2023.go's full
// openb pipeline) uses. It shares buildNodeBase/finalizeNodeCapacity with
// buildNode; see node_job_builders.go.
func buildInputVersionNode(n openbNodeRecord) *corev1.Node {
	node := buildNodeBase(n.name, n.cpuMilli, n.memoryMiB, n.model)
	if n.gpuCount > 0 {
		node.Status.Capacity[corev1.ResourceName(alibabacloudGPUCountKey)] = resource.MustParse(fmt.Sprintf("%d", n.gpuCount))
		node.Status.Capacity[corev1.ResourceName(alibabacloudGPUMilliKey)] = resource.MustParse(fmt.Sprintf("%d", n.gpuCount*gpuMilliPerGPU))
	}
	finalizeNodeCapacity(node)
	return node
}

// gpuMilliPerGPU is the GPU-milli share of a whole GPU (FGD's public
// implementation treats 1000 as one full GPU, see
// pkg/type/open-gpu-share/utils/const.go: ResourceName's comment).
const gpuMilliPerGPU = 1000

// buildInputVersionJob builds the ReplicaSet for one used pod, representing
// its GPU request the way FGD's public implementation does
// (decisions.md's "GPU の annotation の形"): a gpu-milli share (the CSV's
// gpu_milli field, uncapped per spec.md's decision 7, not rounded up to a
// whole GPU) and a gpu-count, both as pod annotations under the
// alibabacloud.com keys, instead of the nvidia.com/gpu extended resource
// request buildJob (gpu2023.go's full openb pipeline) uses. It shares
// buildJobBase with buildJob; see node_job_builders.go.
func buildInputVersionJob(p openbPodRecord) *appsv1.ReplicaSet {
	executionDuration := time.Duration(p.deletionTime-p.scheduledTime) * time.Second
	job := buildJobBase("default-scheduler", p.name, p.cpuMilli, p.memoryMiB, p.gpuSpec, p.qos, executionDuration)

	job.Spec.Template.Annotations[alibabacloudGPUMilliKey] = fmt.Sprintf("%d", p.gpuMilli)
	job.Spec.Template.Annotations[alibabacloudGPUCountKey] = fmt.Sprintf("%d", p.gpuCount)

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

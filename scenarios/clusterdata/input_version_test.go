package clusterdata

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pfnet/kube-scheduler-evaluator-reference/scenarios/consts"
	"github.com/pfnet/kube-scheduler-evaluator/pkg/definition"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

const nodesCSVHeader = "sn,cpu_milli,memory_mib,gpu,model\n"
const podsCSVHeader = "name,cpu_milli,memory_mib,num_gpu,gpu_milli,gpu_spec,qos,pod_phase,creation_time,deletion_time,scheduled_time\n"

func countEventsByType(events []definition.Event, eventType definition.EventType, isNode func(any) bool) int {
	count := 0
	for _, e := range events {
		if e.EventType() == eventType && isNode(e.Object()) {
			count++
		}
	}
	return count
}

func isNodeObject(o any) bool {
	_, ok := o.(*corev1.Node)
	return ok
}

// jobCreateByName returns the ReplicaSet created by events whose name is
// name, or nil if there is no such Create event.
func jobCreateByName(events []definition.Event, name string) *appsv1.ReplicaSet {
	for _, e := range events {
		if e.EventType() != definition.EventTypeCreate {
			continue
		}
		job, ok := e.Object().(*appsv1.ReplicaSet)
		if !ok || job.Name != name {
			continue
		}
		return job
	}
	return nil
}

// jobCreateNames returns the names of the ReplicaSets created by events, in
// event order.
func jobCreateNames(events []definition.Event) []string {
	var names []string
	for _, e := range events {
		if e.EventType() != definition.EventTypeCreate {
			continue
		}
		job, ok := e.Object().(*appsv1.ReplicaSet)
		if !ok {
			continue
		}
		names = append(names, job.Name)
	}
	return names
}

// nodeCreateByName returns the Node created by events whose name is name,
// or nil if there is no such Create event.
func nodeCreateByName(events []definition.Event, name string) *corev1.Node {
	for _, e := range events {
		if e.EventType() != definition.EventTypeCreate {
			continue
		}
		node, ok := e.Object().(*corev1.Node)
		if !ok || node.Name != name {
			continue
		}
		return node
	}
	return nil
}

// nodeCreateNames returns the names of the Nodes created by events, in
// event order.
func nodeCreateNames(events []definition.Event) []string {
	var names []string
	for _, e := range events {
		if e.EventType() != definition.EventTypeCreate {
			continue
		}
		node, ok := e.Object().(*corev1.Node)
		if !ok {
			continue
		}
		names = append(names, node.Name)
	}
	return names
}

func approxEqual(a, b, epsilon float64) bool {
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	return diff <= epsilon
}

// ===== ノードの形ごとの縮め方 =====

// 複数の形があるとき、形ごとに独立して縮める割合が適用される。
func TestBuildInputVersion_ShrinksEachShapeIndependently(t *testing.T) {
	var nodesCSV strings.Builder
	nodesCSV.WriteString(nodesCSVHeader)
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&nodesCSV, "shape-a-%02d,1000,1024,0,\n", i)
	}
	for i := 0; i < 4; i++ {
		fmt.Fprintf(&nodesCSV, "shape-b-%02d,2000,2048,0,\n", i)
	}
	podsCSV := strings.NewReader(podsCSVHeader)

	events, summary, err := BuildInputVersion(strings.NewReader(nodesCSV.String()), podsCSV, 1, 1.0, 0.5)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}

	if summary.SelectedNodeCount != 7 {
		t.Errorf("SelectedNodeCount = %d, want 7 (round(10*0.5)=5 + round(4*0.5)=2)", summary.SelectedNodeCount)
	}
	if got := countEventsByType(events, definition.EventTypeCreate, isNodeObject); got != 7 {
		t.Errorf("node create events = %d, want 7", got)
	}
	if len(summary.NodeShapes) != 2 {
		t.Fatalf("NodeShapes = %+v, want 2 shapes", summary.NodeShapes)
	}
	counts := map[int64]int{}
	for _, shape := range summary.NodeShapes {
		counts[shape.CPUMilli] = shape.Count
	}
	if counts[1000] != 5 {
		t.Errorf("shape cpu=1000 count = %d, want 5", counts[1000])
	}
	if counts[2000] != 2 {
		t.Errorf("shape cpu=2000 count = %d, want 2", counts[2000])
	}
}

// 縮める割合をかけると0台になる形でも、最低1台は残す。
func TestBuildInputVersion_KeepsAtLeastOneNodePerShape(t *testing.T) {
	var nodesCSV strings.Builder
	nodesCSV.WriteString(nodesCSVHeader)
	for i := 0; i < 3; i++ {
		fmt.Fprintf(&nodesCSV, "node-%02d,1000,1024,0,\n", i)
	}
	podsCSV := strings.NewReader(podsCSVHeader)

	_, summary, err := BuildInputVersion(strings.NewReader(nodesCSV.String()), podsCSV, 1, 1.0, 0.05)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}

	if summary.SelectedNodeCount != 1 {
		t.Errorf("SelectedNodeCount = %d, want 1 (round(3*0.05)=0, clamped to 1)", summary.SelectedNodeCount)
	}
}

// 同じ GPU の枚数と型でも、CPU やメモリが違うノードは別の形として縮められる。
func TestBuildInputVersion_SameGPUDifferentCPUMemoryAreDifferentShapes(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader +
		"node-small,16000,131072,2,P100\n" +
		"node-large,32000,262144,2,P100\n")
	podsCSV := strings.NewReader(podsCSVHeader)

	_, summary, err := BuildInputVersion(nodesCSV, podsCSV, 1, 1.0, 1.0)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}

	if len(summary.NodeShapes) != 2 {
		t.Errorf("NodeShapes = %+v, want 2 (same GPU/model but different CPU/memory)", summary.NodeShapes)
	}
}

// 同じシードなら、縮めたときに残るノードの集合が同じになる。
func TestBuildInputVersion_SameSeedProducesSameNodeSelection(t *testing.T) {
	var nodesCSV strings.Builder
	nodesCSV.WriteString(nodesCSVHeader)
	for i := 0; i < 5; i++ {
		fmt.Fprintf(&nodesCSV, "node-%02d,1000,1024,0,\n", i)
	}
	podsCSV := strings.NewReader(podsCSVHeader)

	events1, _, err := BuildInputVersion(strings.NewReader(nodesCSV.String()), strings.NewReader(podsCSVHeader), 42, 1.0, 0.4)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}
	events2, _, err := BuildInputVersion(strings.NewReader(nodesCSV.String()), podsCSV, 42, 1.0, 0.4)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}

	names1 := nodeCreateNames(events1)
	names2 := nodeCreateNames(events2)
	if !slices.Equal(names1, names2) {
		t.Errorf("same seed produced different node selections: %v vs %v", names1, names2)
	}
}

// シードが違うと、縮めたときに残るノードの集合が違う。
func TestBuildInputVersion_DifferentSeedsProduceDifferentNodeSelection(t *testing.T) {
	var nodesCSV strings.Builder
	nodesCSV.WriteString(nodesCSVHeader)
	for i := 0; i < 5; i++ {
		fmt.Fprintf(&nodesCSV, "node-%02d,1000,1024,0,\n", i)
	}

	eventsSeed1, _, err := BuildInputVersion(strings.NewReader(nodesCSV.String()), strings.NewReader(podsCSVHeader), 1, 1.0, 0.4)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}
	eventsSeed2, _, err := BuildInputVersion(strings.NewReader(nodesCSV.String()), strings.NewReader(podsCSVHeader), 2, 1.0, 0.4)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}

	namesSeed1 := nodeCreateNames(eventsSeed1)
	namesSeed2 := nodeCreateNames(eventsSeed2)
	if slices.Equal(namesSeed1, namesSeed2) {
		t.Errorf("different seeds (1, 2) produced the same node selection: %v", namesSeed1)
	}
}

// ===== Pod の扱い（Pending・Failed・使った件数） =====

// 入力の版は、ノードの CSV にある行をすべて使う。
func TestBuildInputVersion_UsesAllNodes(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader +
		"openb-node-0000,32000,262144,0,\n" +
		"openb-node-0001,32000,262144,0,\n")
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-0000,1000,1024,0,0,,LS,Running,0,100,10\n" +
		"openb-pod-0001,1000,1024,0,0,,LS,Running,50,150,60\n")

	events, summary, err := BuildInputVersion(nodesCSV, podsCSV, 1, 0.0001, 1.0)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}

	nodeCreates := countEventsByType(events, definition.EventTypeCreate, isNodeObject)
	if nodeCreates != 2 {
		t.Errorf("node create events = %d, want 2", nodeCreates)
	}
	if summary.UsedPodCount != 2 {
		t.Errorf("UsedPodCount = %d, want 2", summary.UsedPodCount)
	}
}

// pod_phase が Pending の Pod は入力の版から除く。
func TestBuildInputVersion_ExcludesPendingPods(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,0,\n")
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-pending,1000,1024,0,0,,LS,Pending,0,100,10\n" +
		"openb-pod-running,1000,1024,0,0,,LS,Running,0,100,10\n" +
		"openb-pod-running2,1000,1024,0,0,,LS,Running,50,150,60\n")

	events, summary, err := BuildInputVersion(nodesCSV, podsCSV, 1, 0.0001, 1.0)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}

	names := jobCreateNames(events)
	if slices.Contains(names, "openb-pod-pending") {
		t.Errorf("job create names = %v, want it to not contain openb-pod-pending", names)
	}
	if summary.UsedPodCount != 2 {
		t.Errorf("UsedPodCount = %d, want 2", summary.UsedPodCount)
	}
	if summary.ExcludedPendingPodCount != 1 {
		t.Errorf("ExcludedPendingPodCount = %d, want 1", summary.ExcludedPendingPodCount)
	}
}

// pod_phase が Failed の Pod は、ほかの Pod と同じく入力の版に残り、実行時間が
// 経ったら完了する対象として数える。
func TestBuildInputVersion_KeepsFailedPods(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,0,\n")
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-failed,1000,1024,0,0,,LS,Failed,0,100,10\n" +
		"openb-pod-running,1000,1024,0,0,,LS,Running,0,100,10\n" +
		"openb-pod-running2,1000,1024,0,0,,LS,Running,50,150,60\n")

	events, summary, err := BuildInputVersion(nodesCSV, podsCSV, 1, 0.0001, 1.0)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}

	names := jobCreateNames(events)
	if len(names) != 3 {
		t.Errorf("job create names = %v, want 3 names (failed pod kept)", names)
	}
	if summary.UsedPodCount != 3 {
		t.Errorf("UsedPodCount = %d, want 3", summary.UsedPodCount)
	}
	if summary.FailedPodCount != 1 {
		t.Errorf("FailedPodCount = %d, want 1", summary.FailedPodCount)
	}
}

// 実行時間は、deletion_time から scheduled_time を引いた値にする。
func TestBuildInputVersion_ExecutionDurationIsDeletionMinusScheduled(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,0,\n")
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-0000,1000,1024,0,0,,LS,Running,0,100,10\n" +
		"openb-pod-0001,1000,1024,0,0,,LS,Running,50,150,60\n")

	events, _, err := BuildInputVersion(nodesCSV, podsCSV, 1, 0.0001, 1.0)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}

	job := jobCreateByName(events, "openb-pod-0000")
	if job == nil {
		t.Fatalf("no create event for openb-pod-0000")
	}
	key := definition.ExecutionDurationAnnotationKey(consts.UserID)
	got := job.Spec.Template.Annotations[key]
	want := (90 * time.Second).String()
	if got != want {
		t.Errorf("execution duration annotation = %q, want %q", got, want)
	}
}

// ===== GPU の annotation =====

// Pod の GPU 要求は、CSV の num_gpu と gpu_milli の値どおりに
// alibabacloud.com/gpu-count と alibabacloud.com/gpu-milli の注釈になる
// （FGD の公開の実装と同じ形。decisions.md「GPU の annotation の形」）。
func TestBuildInputVersion_PodGPUAnnotationsMatchCSV(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,2,V100M16\n")
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-0000,1000,1024,2,500,,LS,Running,0,100,10\n" +
		"openb-pod-0001,1000,1024,0,0,,LS,Running,50,150,60\n")

	events, _, err := BuildInputVersion(nodesCSV, podsCSV, 1, 0.0001, 1.0)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}

	job := jobCreateByName(events, "openb-pod-0000")
	if job == nil {
		t.Fatalf("no create event for openb-pod-0000")
	}
	annotations := job.Spec.Template.Annotations
	if got := annotations["alibabacloud.com/gpu-count"]; got != "2" {
		t.Errorf("gpu-count annotation = %q, want %q", got, "2")
	}
	if got := annotations["alibabacloud.com/gpu-milli"]; got != "500" {
		t.Errorf("gpu-milli annotation = %q, want %q", got, "500")
	}
}

// GPU を要求する Pod の資源の要求には、nvidia.com/gpu のような拡張リソースは
// 無い（GPU は annotation だけで表す）。
func TestBuildInputVersion_PodHasNoGPUExtendedResourceRequest(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,2,V100M16\n")
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-0000,1000,1024,2,500,,LS,Running,0,100,10\n" +
		"openb-pod-anchor,1000,1024,0,0,,LS,Running,50,150,60\n")

	events, _, err := BuildInputVersion(nodesCSV, podsCSV, 1, 0.0001, 1.0)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}

	job := jobCreateByName(events, "openb-pod-0000")
	if job == nil {
		t.Fatalf("no create event for openb-pod-0000")
	}
	requests := job.Spec.Template.Spec.Containers[0].Resources.Requests
	if _, ok := requests[corev1.ResourceName("nvidia.com/gpu")]; ok {
		t.Errorf("requests = %v, want no nvidia.com/gpu extended resource", requests)
	}
}

// ノードの GPU の容量は、alibabacloud.com/gpu-count と
// alibabacloud.com/gpu-milli（1枚あたり1000）で表す。nvidia.com/gpu の
// 拡張リソースは使わない。
func TestBuildInputVersion_NodeCapacityUsesAlibabacloudGPUKeys(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,4,V100M16\n")
	podsCSV := strings.NewReader(podsCSVHeader)

	events, _, err := BuildInputVersion(nodesCSV, podsCSV, 1, 1.0, 1.0)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}

	node := nodeCreateByName(events, "openb-node-0000")
	if node == nil {
		t.Fatalf("no create event for openb-node-0000")
	}
	gpuCount := node.Status.Capacity[corev1.ResourceName("alibabacloud.com/gpu-count")]
	if got := gpuCount.Value(); got != 4 {
		t.Errorf("gpu-count capacity = %d, want 4", got)
	}
	gpuMilli := node.Status.Capacity[corev1.ResourceName("alibabacloud.com/gpu-milli")]
	if got := gpuMilli.Value(); got != 4000 {
		t.Errorf("gpu-milli capacity = %d, want 4000", got)
	}
	if _, ok := node.Status.Capacity[corev1.ResourceName("nvidia.com/gpu")]; ok {
		t.Errorf("capacity = %v, want no nvidia.com/gpu extended resource", node.Status.Capacity)
	}
	allocGPUCount := node.Status.Allocatable[corev1.ResourceName("alibabacloud.com/gpu-count")]
	if got := allocGPUCount.Value(); got != 4 {
		t.Errorf("gpu-count allocatable = %d, want 4", got)
	}
}

// gpu_spec が空でない Pod には、GPU の型のラベルが指定された型のどれかに
// 一致するノードにだけ置けるよう nodeAffinity を付ける。
func TestBuildInputVersion_AddsNodeAffinityFromGPUSpec(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,1,V100M16\n")
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-0000,1000,1024,1,1000,V100M16|V100M32,LS,Running,0,100,10\n" +
		"openb-pod-0001,1000,1024,0,0,,LS,Running,50,150,60\n")

	events, _, err := BuildInputVersion(nodesCSV, podsCSV, 1, 0.0001, 1.0)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}

	job := jobCreateByName(events, "openb-pod-0000")
	if job == nil {
		t.Fatalf("no create event for openb-pod-0000")
	}
	affinity := job.Spec.Template.Spec.Affinity
	if affinity == nil || affinity.NodeAffinity == nil ||
		affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		t.Fatalf("no nodeAffinity set, got %+v", affinity)
	}
	terms := affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	if len(terms) != 1 || len(terms[0].MatchExpressions) != 1 {
		t.Fatalf("unexpected nodeSelectorTerms: %+v", terms)
	}
	expr := terms[0].MatchExpressions[0]
	wantKey := fmt.Sprintf("%s/model", consts.UserID)
	if expr.Key != wantKey {
		t.Errorf("matchExpression key = %q, want %q", expr.Key, wantKey)
	}
	if expr.Operator != corev1.NodeSelectorOpIn {
		t.Errorf("matchExpression operator = %q, want In", expr.Operator)
	}
	wantValues := []string{"V100M16", "V100M32"}
	if !slices.Equal(expr.Values, wantValues) {
		t.Errorf("matchExpression values = %v, want %v", expr.Values, wantValues)
	}
}

// gpu_spec が空の Pod には nodeAffinity を付けない。
func TestBuildInputVersion_NoNodeAffinityWithoutGPUSpec(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,0,\n")
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-0000,1000,1024,0,0,,LS,Running,0,100,10\n" +
		"openb-pod-0001,1000,1024,0,0,,LS,Running,50,150,60\n")

	events, _, err := BuildInputVersion(nodesCSV, podsCSV, 1, 0.0001, 1.0)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}

	job := jobCreateByName(events, "openb-pod-0000")
	if job == nil {
		t.Fatalf("no create event for openb-pod-0000")
	}
	if job.Spec.Template.Spec.Affinity != nil {
		t.Errorf("affinity = %+v, want nil", job.Spec.Template.Spec.Affinity)
	}
}

// ===== Pod の順序 =====

func tenEligiblePodsCSV() *strings.Reader {
	var b strings.Builder
	b.WriteString(podsCSVHeader)
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&b, "openb-pod-%04d,1000,1024,0,0,,LS,Running,%d,%d,%d\n", i, i*10, i*10+100, i*10+10)
	}
	return strings.NewReader(b.String())
}

func sixPodsAtTheSameCreationTimeCSV() *strings.Reader {
	var b strings.Builder
	b.WriteString(podsCSVHeader)
	for i := 0; i < 6; i++ {
		fmt.Fprintf(&b, "openb-pod-%04d,1000,1024,0,0,,LS,Running,0,100,10\n", i)
	}
	b.WriteString("openb-pod-anchor,1000,1024,0,0,,LS,Running,1000000,1000100,1000010\n")
	return strings.NewReader(b.String())
}

// 同じシードなら、使う Pod の順が同じになる。
func TestBuildInputVersion_SameSeedProducesSameOrder(t *testing.T) {
	nodesCSV1 := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,0,\n")
	nodesCSV2 := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,0,\n")

	events1, _, err := BuildInputVersion(nodesCSV1, tenEligiblePodsCSV(), 42, 0.0001, 1.0)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}
	events2, _, err := BuildInputVersion(nodesCSV2, tenEligiblePodsCSV(), 42, 0.0001, 1.0)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}

	names1 := jobCreateNames(events1)
	names2 := jobCreateNames(events2)
	if !slices.Equal(names1, names2) {
		t.Errorf("same seed produced different orders: %v vs %v", names1, names2)
	}
}

// 同じ creation_time の Pod は、間隔0で同じ時刻に投入されるが、シードで
// 投入の順が並べ替えられる。
func TestBuildInputVersion_ShufflesOrderOfSameCreationTime(t *testing.T) {
	nodesCSV1 := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,0,\n")
	nodesCSV2 := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,0,\n")
	nodesCSV3 := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,0,\n")

	eventsSeed1a, _, err := BuildInputVersion(nodesCSV1, sixPodsAtTheSameCreationTimeCSV(), 1, 0.0001, 1.0)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}
	eventsSeed1b, _, err := BuildInputVersion(nodesCSV3, sixPodsAtTheSameCreationTimeCSV(), 1, 0.0001, 1.0)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}
	eventsSeed2, _, err := BuildInputVersion(nodesCSV2, sixPodsAtTheSameCreationTimeCSV(), 2, 0.0001, 1.0)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}

	namesSeed1a := jobCreateNames(eventsSeed1a)
	namesSeed1b := jobCreateNames(eventsSeed1b)
	namesSeed2 := jobCreateNames(eventsSeed2)

	if !slices.Equal(namesSeed1a, namesSeed1b) {
		t.Errorf("same seed produced different orders: %v vs %v", namesSeed1a, namesSeed1b)
	}
	if slices.Equal(namesSeed1a, namesSeed2) {
		t.Errorf("different seeds (1, 2) produced the same order: %v", namesSeed1a)
	}
}

// Pod は creation_time の間隔で投入される。evaluator はイベントに付けた
// interval をそのイベント自身の発火の前に足す（イベント自身の待ち時間であり、
// 次のイベントまでの待ち時間ではない）ので、最初の Pod の interval は0、
// それ以降は直前の Pod との creation_time の差になる。この CSV は、実行時間
// 10sが投入期間[0,100]への重なり（pod0は100、pod1は70、pod2は0）より短い
// ので重なりが実行時間で飽和し、cpuのρ(倍率1)=1000*(10+10)/(32000*100)=
// 0.00625になる。targetRho に同じ値を渡せば倍率が1になり、間隔はそのまま
// creation_time の差になる。
func TestBuildInputVersion_SubmissionIntervalIsCreationTimeGap(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,0,\n")
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-0000,1000,1024,0,0,,LS,Running,0,10,0\n" +
		"openb-pod-0001,1000,1024,0,0,,LS,Running,30,40,30\n" +
		"openb-pod-0002,1000,1024,0,0,,LS,Running,100,110,100\n")

	events, _, err := BuildInputVersion(nodesCSV, podsCSV, 1, 0.00625, 1.0)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}

	var intervals []time.Duration
	for _, e := range events {
		if _, ok := e.Object().(*appsv1.ReplicaSet); ok && e.EventType() == definition.EventTypeCreate {
			intervals = append(intervals, e.Interval())
		}
	}
	if len(intervals) != 3 {
		t.Fatalf("got %d pod create events, want 3", len(intervals))
	}
	if intervals[0] != 0 {
		t.Errorf("intervals[0] = %v, want 0 (first pod has no preceding gap)", intervals[0])
	}
	if intervals[1] != 30*time.Second {
		t.Errorf("intervals[1] = %v, want 30s", intervals[1])
	}
	if intervals[2] != 70*time.Second {
		t.Errorf("intervals[2] = %v, want 70s", intervals[2])
	}
}

// ===== 定義 A の ρ と倍率 =====

// ρ の需要は、各Podの実行時間そのものではなく、投入の時刻から実行時間だけ
// 続く区間のうち投入期間に重なる長さを使う（glossary の定義A）。この CSV
// では pod0 の実行時間は1000sだが、投入期間は[0,100]で、pod0 の creation_time
// (0) から投入期間の終わり(100)までは100sしかないので、重なりは実行時間
// 1000sではなく100sになる。もし重なりを考えず実行時間そのものを使うと
// cpuのρは1000*1000/(1000*100)=10.0になってしまうが、重なりを使うと
// 1000*100/(1000*100)=1.0になる。
func TestBuildInputVersion_DemandIsClampedToOverlapWithSubmissionPeriod(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,1000,0,0,\n")
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-0000,1000,0,0,0,,LS,Running,0,1000,0\n" +
		"openb-pod-0001,1000,0,0,0,,LS,Running,100,101,100\n")

	_, summary, err := BuildInputVersion(nodesCSV, podsCSV, 1, 1.0, 1.0)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}

	if !approxEqual(summary.RhoBeforeScaling.CPUMilli, 1.0, 1e-9) {
		t.Errorf("RhoBeforeScaling.CPUMilli = %v, want 1.0 (not 10.0, which is what an unclamped duration would give)", summary.RhoBeforeScaling.CPUMilli)
	}
}

// ρ は資源ごとに求め、最大の値をとる。この CSV では、cpu・メモリの比は
// 無視できるほど小さく、GPU の比（割合での要求を切り上げずに使う。pod0・pod1
// はどちらも num_gpu=2, gpu_milli=1000 なので実効GPUは2.0で、実行時間10sが
// 投入期間[0,100]に完全に重なる）が最大になる: gpuのρ = 2.0*(10+10) /
// (2*100) = 0.2。
func TestBuildInputVersion_RhoIsDrivenByGPUWhenItIsTheBottleneck(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,2,\n")
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-0000,1,1,2,1000,,LS,Running,0,10,0\n" +
		"openb-pod-0001,1,1,2,1000,,LS,Running,50,60,50\n" +
		"openb-pod-anchor,1,1,0,0,,LS,Running,100,100,100\n")

	_, summary, err := BuildInputVersion(nodesCSV, podsCSV, 1, 0.4, 1.0)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}
	if !approxEqual(summary.RhoBeforeScaling.GPU, 0.2, 1e-9) {
		t.Errorf("RhoBeforeScaling.GPU = %v, want 0.2", summary.RhoBeforeScaling.GPU)
	}
	if !approxEqual(summary.RhoAfterScaling.GPU, 0.4, 1e-6) {
		t.Errorf("RhoAfterScaling.GPU = %v, want 0.4 (the target)", summary.RhoAfterScaling.GPU)
	}
	if !approxEqual(summary.Multiplier, 0.5, 1e-6) {
		t.Errorf("Multiplier = %v, want 0.5", summary.Multiplier)
	}
}

// 倍率をかけた後のρは、目標のρに一致する（重なりが実行時間で飽和している、
// つまり倍率で圧縮しても実行時間のほうが先に効いてこない範囲での単純な例）。
// pod0・pod1はどちらもcpu=1000, 実行時間10sで、投入期間[0,100]のうち
// pod0の重なりは100、pod1は50（実行時間10sより大きいので実行時間10sで
// 飽和）。cpuのρ(倍率1) = 1000*(10+10) / (10000*100) = 0.02。
func TestBuildInputVersion_RhoBeforeAndAfterScaling(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,10000,0,0,\n")
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-0000,1000,0,0,0,,LS,Running,0,10,0\n" +
		"openb-pod-0001,1000,0,0,0,,LS,Running,50,60,50\n" +
		"openb-pod-anchor,1000,0,0,0,,LS,Running,100,100,100\n")

	_, summary, err := BuildInputVersion(nodesCSV, podsCSV, 1, 0.05, 1.0)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}

	if !approxEqual(summary.RhoBeforeScaling.CPUMilli, 0.02, 1e-9) {
		t.Errorf("RhoBeforeScaling.CPUMilli = %v, want 0.02", summary.RhoBeforeScaling.CPUMilli)
	}
	if !approxEqual(summary.RhoAfterScaling.CPUMilli, 0.05, 1e-6) {
		t.Errorf("RhoAfterScaling.CPUMilli = %v, want 0.05 (the target)", summary.RhoAfterScaling.CPUMilli)
	}
	if !approxEqual(summary.Multiplier, 0.4, 1e-6) {
		t.Errorf("Multiplier = %v, want 0.4", summary.Multiplier)
	}
	// 投入期間は、倍率をかける前の100sではなく、倍率(0.4)をかけた後の
	// 40sになる。入力の版が実際に投入される期間はこちら。二分探索で求めた
	// 倍率はぴったり0.4ではないので、1ミリ秒の許容誤差で比べる。
	if diff := summary.SubmissionPeriod - 40*time.Second; diff > time.Millisecond || diff < -time.Millisecond {
		t.Errorf("SubmissionPeriod = %v, want ~40s (period 100s scaled by the multiplier 0.4)", summary.SubmissionPeriod)
	}
}

// ρ を求める容量は、すべてのノードの容量の合計である。
func TestBuildInputVersion_RhoUsesCapacitySummedAcrossNodes(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader +
		"openb-node-0000,5000,0,0,\n" +
		"openb-node-0001,5000,0,0,\n")
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-0000,1000,0,0,0,,LS,Running,0,10,0\n" +
		"openb-pod-0001,1000,0,0,0,,LS,Running,50,60,50\n" +
		"openb-pod-anchor,1000,0,0,0,,LS,Running,100,100,100\n")

	_, summary, err := BuildInputVersion(nodesCSV, podsCSV, 1, 0.05, 1.0)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}
	if !approxEqual(summary.RhoBeforeScaling.CPUMilli, 0.02, 1e-9) {
		t.Errorf("RhoBeforeScaling.CPUMilli = %v, want 0.02", summary.RhoBeforeScaling.CPUMilli)
	}
}

// 資源ごとのρはメモリでも同じ式で求まる。cpuとgpuの容量は十分大きくして
// 比を無視できるほど小さくし、メモリだけを逼迫させる。pod0・pod1はどちらも
// memory=1000, 実行時間10sで、投入期間[0,100]のうちpod0の重なりは100、
// pod1は50（実行時間10sより大きいので実行時間10sで飽和）。メモリのρ
// (倍率1) = 1000*(10+10) / (10000*100) = 0.02。
func TestBuildInputVersion_RhoBeforeAndAfterScaling_Memory(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,1000000,10000,0,\n")
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-0000,1,1000,0,0,,LS,Running,0,10,0\n" +
		"openb-pod-0001,1,1000,0,0,,LS,Running,50,60,50\n" +
		"openb-pod-anchor,1,1000,0,0,,LS,Running,100,100,100\n")

	_, summary, err := BuildInputVersion(nodesCSV, podsCSV, 1, 0.05, 1.0)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}

	if !approxEqual(summary.RhoBeforeScaling.MemoryMiB, 0.02, 1e-9) {
		t.Errorf("RhoBeforeScaling.MemoryMiB = %v, want 0.02", summary.RhoBeforeScaling.MemoryMiB)
	}
	if !approxEqual(summary.RhoAfterScaling.MemoryMiB, 0.05, 1e-6) {
		t.Errorf("RhoAfterScaling.MemoryMiB = %v, want 0.05 (the target)", summary.RhoAfterScaling.MemoryMiB)
	}
}

// 目標のρが今のρより低いときは、倍率を1より大きくして投入期間を伸ばす
// 側に探索する。TestBuildInputVersion_RhoBeforeAndAfterScalingと同じCSV
// (cpuのρ(倍率1)=0.02)に、それより低い目標(0.01)を渡すと、飽和した重なり
// のまま期間だけが伸びるので、倍率は0.02/0.01=2.0になる。
func TestBuildInputVersion_MultiplierCanExceedOneWhenTargetIsBelowCurrentRho(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,10000,0,0,\n")
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-0000,1000,0,0,0,,LS,Running,0,10,0\n" +
		"openb-pod-0001,1000,0,0,0,,LS,Running,50,60,50\n" +
		"openb-pod-anchor,1000,0,0,0,,LS,Running,100,100,100\n")

	_, summary, err := BuildInputVersion(nodesCSV, podsCSV, 1, 0.01, 1.0)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}

	if !approxEqual(summary.Multiplier, 2.0, 1e-6) {
		t.Errorf("Multiplier = %v, want 2.0 (greater than 1)", summary.Multiplier)
	}
	if !approxEqual(summary.RhoAfterScaling.CPUMilli, 0.01, 1e-6) {
		t.Errorf("RhoAfterScaling.CPUMilli = %v, want 0.01 (the target)", summary.RhoAfterScaling.CPUMilli)
	}
}

// 目標のρが極端に低いと、理屈のうえでは届く倍率があっても、探索の上限
// (findMultiplierのmaxHigh)を超えてしまう。この安全装置が働いて、無限に
// 倍率を伸ばし続けずにエラーで返す。
func TestBuildInputVersion_ErrorsWhenMultiplierWouldExceedSearchLimit(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,10000,0,0,\n")
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-0000,1000,0,0,0,,LS,Running,0,10,0\n" +
		"openb-pod-0001,1000,0,0,0,,LS,Running,50,60,50\n" +
		"openb-pod-anchor,1000,0,0,0,,LS,Running,100,100,100\n")

	_, _, err := BuildInputVersion(nodesCSV, podsCSV, 1, 1e-20, 1.0)
	if err == nil {
		t.Fatal("BuildInputVersion returned no error for a target rho requiring a multiplier beyond the search limit")
	}
	if !strings.Contains(err.Error(), "could not find a multiplier") {
		t.Errorf("error = %q, want it to say a multiplier could not be found", err)
	}
}

// 縮めたクラスタでも目標のρに届かないときは、定義Aの重なりによる頭打ち
// （spec.mdの決まり7が1213台のρについて述べているのと同じ現象）が起きて
// いることが伝わるエラーで返す。このCSVでは、重なりが実行時間で飽和しない
// 倍率の範囲（倍率が小さいほど重なりが小さくなる）で頭打ちの値（0.15）が
// 目標(1.0)より低い。
func TestBuildInputVersion_ErrorsOnUnreachableTargetRho(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,10000,0,0,\n")
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-0000,1000,0,0,0,,LS,Running,0,10,0\n" +
		"openb-pod-0001,1000,0,0,0,,LS,Running,50,60,50\n" +
		"openb-pod-anchor,1000,0,0,0,,LS,Running,100,100,100\n")

	_, _, err := BuildInputVersion(nodesCSV, podsCSV, 1, 1.0, 1.0)
	if err == nil {
		t.Fatal("BuildInputVersion returned no error for an unreachable target rho")
	}
	if !strings.Contains(err.Error(), "unreachable") {
		t.Errorf("error = %q, want it to say the target rho is unreachable", err)
	}
}

// deletion_time が scheduled_time より前という、実行時間が負になる壊れた
// 行があればエラーで返す。
func TestBuildInputVersion_ErrorsOnNegativeExecutionDuration(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,0,\n")
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-0000,1000,1024,0,0,,LS,Running,0,10,20\n")

	_, _, err := BuildInputVersion(nodesCSV, podsCSV, 1, 1.0, 1.0)
	if err == nil {
		t.Fatal("BuildInputVersion returned no error for a negative execution duration")
	}
}

// openb の実データでは、Pending の Pod は scheduled_time が空になる。
// Pending は入力の版から除くので、このフィールドが空でも失敗しない。
func TestBuildInputVersion_PendingPodWithEmptyScheduledTimeDoesNotError(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,0,\n")
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-pending,1000,1024,0,0,,LS,Pending,0,100,\n" +
		"openb-pod-running,1000,1024,0,0,,LS,Running,0,100,10\n" +
		"openb-pod-running2,1000,1024,0,0,,LS,Running,50,150,60\n")

	_, summary, err := BuildInputVersion(nodesCSV, podsCSV, 1, 0.0001, 1.0)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}
	if summary.UsedPodCount != 2 {
		t.Errorf("UsedPodCount = %d, want 2", summary.UsedPodCount)
	}
}

// すべての Pod が Pending だと使う Pod が0件になるが、これは正当な入力
// なのでエラーにはせず、件数とρがすべて0になる。
func TestBuildInputVersion_AllPodsPendingUsesNoPods(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,0,\n")
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-0000,1000,1024,0,0,,LS,Pending,0,100,\n" +
		"openb-pod-0001,1000,1024,0,0,,LS,Pending,50,150,\n")

	events, summary, err := BuildInputVersion(nodesCSV, podsCSV, 1, 1.0, 1.0)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}

	if summary.UsedPodCount != 0 {
		t.Errorf("UsedPodCount = %d, want 0", summary.UsedPodCount)
	}
	if len(jobCreateNames(events)) != 0 {
		t.Errorf("job create events = %v, want none", jobCreateNames(events))
	}
	if summary.RhoBeforeScaling.Max() != 0 {
		t.Errorf("RhoBeforeScaling = %+v, want all 0", summary.RhoBeforeScaling)
	}
	if summary.RhoAfterScaling.Max() != 0 {
		t.Errorf("RhoAfterScaling = %+v, want all 0", summary.RhoAfterScaling)
	}
}

// scheduled_time が空になるのは openb の実データでは Pending の Pod だけ
// なので、Pending 以外の行で空だったら（データが壊れている）エラーにする。
func TestBuildInputVersion_ErrorsOnEmptyScheduledTimeForNonPendingPod(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,0,\n")
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-0000,1000,1024,0,0,,LS,Running,0,100,\n")

	_, _, err := BuildInputVersion(nodesCSV, podsCSV, 1, 1.0, 1.0)
	if err == nil {
		t.Fatal("BuildInputVersion returned no error for a non-Pending pod with empty scheduled_time")
	}
}

// 複数の Pod があっても creation_time がすべて同じだと投入期間が0になり、
// ρ を求められない。根拠の無い0ではなく、エラーとして返す。
func TestBuildInputVersion_ErrorsOnZeroSubmissionPeriod(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,0,\n")
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-0000,1000,1024,0,0,,LS,Running,0,100,10\n" +
		"openb-pod-0001,1000,1024,0,0,,LS,Running,0,100,10\n")

	_, _, err := BuildInputVersion(nodesCSV, podsCSV, 1, 1.0, 1.0)
	if err == nil {
		t.Fatal("BuildInputVersion returned no error for a zero submission period")
	}
}

// 使う Pod があるのに、どのノードもどの資源の容量も0だと、ρ の分母が0に
// なって求められない。容量が無いことが伝わるエラーで返す。使う Pod の
// 要求もすべて0にして、置けないPodのエラー（U02の別の検査）より先にこの
// エラーに当たるようにする。
func TestBuildInputVersion_ErrorsOnZeroNodeCapacity(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader +
		"openb-node-0000,0,0,0,\n" +
		"openb-node-0001,0,0,0,\n")
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-0000,0,0,0,0,,LS,Running,0,100,10\n" +
		"openb-pod-0001,0,0,0,0,,LS,Running,50,150,60\n")

	_, _, err := BuildInputVersion(nodesCSV, podsCSV, 1, 1.0, 1.0)
	if err == nil {
		t.Fatal("BuildInputVersion returned no error for nodes without any capacity")
	}
	if !strings.Contains(err.Error(), "no node has any capacity") {
		t.Errorf("error = %q, want it to say that no node has any capacity", err)
	}
}

// 使う Pod があるのに要求量×重なりの合計がどの資源でも0だと、ρ が
// 根拠無く0になり、倍率0で投入の間隔がすべて0の入力の版が目標の ρ を
// 満たしたように見えてしまう。これはエラーで返す。
func TestBuildInputVersion_ErrorsOnZeroDemand(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,0,\n")
	// deletion_time と scheduled_time が同じなので、どの Pod も実行時間0。
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-0000,1000,1024,0,0,,LS,Running,0,10,10\n" +
		"openb-pod-0001,1000,1024,0,0,,LS,Running,50,60,60\n")

	_, _, err := BuildInputVersion(nodesCSV, podsCSV, 1, 1.0, 1.0)
	if err == nil {
		t.Fatal("BuildInputVersion returned no error for a zero total demand")
	}
}

// ===== 置けない Pod =====

// 縮めたクラスタのどのノードの容量も超える要求のPodがあれば、入力の版を
// 作る時点でエラーにする。
func TestBuildInputVersion_ErrorsOnPodExceedingEveryNodeCapacity(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,1000,1024,0,\n")
	podsCSV := strings.NewReader(podsCSVHeader + "openb-pod-0000,2000,1024,0,0,,LS,Running,0,100,10\n")

	_, _, err := BuildInputVersion(nodesCSV, podsCSV, 1, 1.0, 1.0)
	if err == nil {
		t.Fatal("BuildInputVersion returned no error for a pod exceeding every node's cpu capacity")
	}
	if !strings.Contains(err.Error(), "openb-pod-0000") {
		t.Errorf("error = %q, want it to name the pod", err)
	}
}

// 容量不足はメモリだけでも検査する。CPU・GPUが収まっても、メモリが
// どのノードの容量も超えればエラーにする。
func TestBuildInputVersion_ErrorsOnPodExceedingEveryNodeMemory(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,2000,1024,0,\n")
	podsCSV := strings.NewReader(podsCSVHeader + "openb-pod-0000,1000,2048,0,0,,LS,Running,0,100,10\n")

	_, _, err := BuildInputVersion(nodesCSV, podsCSV, 1, 1.0, 1.0)
	if err == nil {
		t.Fatal("BuildInputVersion returned no error for a pod exceeding every node's memory capacity")
	}
	if !strings.Contains(err.Error(), "openb-pod-0000") {
		t.Errorf("error = %q, want it to name the pod", err)
	}
}

// 容量不足はGPUの枚数でも検査する。CPU・メモリが収まっても、GPUの枚数が
// どのノードの容量も超えればエラーにする。
func TestBuildInputVersion_ErrorsOnPodExceedingEveryNodeGPUCount(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,1,V100M16\n")
	podsCSV := strings.NewReader(podsCSVHeader + "openb-pod-0000,1000,1024,2,1000,,LS,Running,0,100,10\n")

	_, _, err := BuildInputVersion(nodesCSV, podsCSV, 1, 1.0, 1.0)
	if err == nil {
		t.Fatal("BuildInputVersion returned no error for a pod exceeding every node's gpu count")
	}
}

// ===== パラメータの検証 =====

// nodeShrinkRatio が0〜1の範囲を外れると、不正な入力としてエラーを返す。
func TestBuildInputVersion_ErrorsOnNodeShrinkRatioOutOfRange(t *testing.T) {
	for _, ratio := range []float64{-0.5, 1.5} {
		nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,0,\n")
		podsCSV := strings.NewReader(podsCSVHeader +
			"openb-pod-0000,1000,1024,0,0,,LS,Running,0,100,10\n")

		_, _, err := BuildInputVersion(nodesCSV, podsCSV, 1, 1.0, ratio)
		if err == nil {
			t.Errorf("ratio=%v: BuildInputVersion returned no error", ratio)
		}
	}
}

// 目標の ρ が0以下だと、倍率が無視されたことが研究者に伝わらないまま
// 成功してしまうので、不正な入力としてエラーを返す。
func TestBuildInputVersion_ErrorsOnNonPositiveTargetRho(t *testing.T) {
	for _, targetRho := range []float64{0, -1} {
		nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,0,\n")
		podsCSV := strings.NewReader(podsCSVHeader +
			"openb-pod-0000,1000,1024,0,0,,LS,Running,0,100,10\n")

		_, _, err := BuildInputVersion(nodesCSV, podsCSV, 1, targetRho, 1.0)
		if err == nil {
			t.Errorf("targetRho=%v: BuildInputVersion returned no error", targetRho)
		}
	}
}

// nodeShrinkRatio が NaN だと、どの比較も偽になって範囲の検査をすり抜けて
// しまう。NaN は範囲を外れた値として、エラーで返す。
func TestBuildInputVersion_ErrorsOnNaNNodeShrinkRatio(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,0,\n")
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-0000,1000,1024,0,0,,LS,Running,0,100,10\n" +
		"openb-pod-0001,1000,1024,0,0,,LS,Running,50,150,60\n")

	_, _, err := BuildInputVersion(nodesCSV, podsCSV, 1, 1.0, math.NaN())
	if err == nil {
		t.Fatal("BuildInputVersion returned no error for a NaN nodeShrinkRatio")
	}
}

// 目標の ρ が NaN だと、不正な値として、エラーで返す。
func TestBuildInputVersion_ErrorsOnNaNTargetRho(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,0,\n")
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-0000,1000,1024,0,0,,LS,Running,0,100,10\n" +
		"openb-pod-0001,1000,1024,0,0,,LS,Running,50,150,60\n")

	_, _, err := BuildInputVersion(nodesCSV, podsCSV, 1, math.NaN(), 1.0)
	if err == nil {
		t.Fatal("BuildInputVersion returned no error for a NaN targetRho")
	}
}

// ===== CSV のパース =====

// buildInputVersionJob が作る Job の cpu・メモリの要求は、CSV の値どおりに
// なる。
func TestBuildInputVersion_JobCPUMemoryRequestsMatchCSV(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,3,\n")
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-0000,1234,5678,3,0,,LS,Running,0,100,10\n" +
		"openb-pod-0001,1000,1024,0,0,,LS,Running,50,150,60\n")

	events, _, err := BuildInputVersion(nodesCSV, podsCSV, 1, 0.0001, 1.0)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}

	job := jobCreateByName(events, "openb-pod-0000")
	if job == nil {
		t.Fatalf("no create event for openb-pod-0000")
	}
	requests := job.Spec.Template.Spec.Containers[0].Resources.Requests
	if got := requests.Cpu().MilliValue(); got != 1234 {
		t.Errorf("cpu request = %dm, want 1234m", got)
	}
	if got := requests.Memory().Value(); got != 5678*1024*1024 {
		t.Errorf("memory request = %d bytes, want %d bytes", got, 5678*1024*1024)
	}
}

// 数値であるべき列が数字でない Pod の行は、エラーで返す。
func TestBuildInputVersion_ErrorsOnNonNumericPodField(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,0,\n")
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-0000,not-a-number,1024,0,0,,LS,Running,0,100,10\n")

	_, _, err := BuildInputVersion(nodesCSV, podsCSV, 1, 1.0, 1.0)
	if err == nil {
		t.Fatal("BuildInputVersion returned no error for a non-numeric cpu_milli")
	}
}

// gpu_milli が数字でない Pod の行も、エラーで返す。
func TestBuildInputVersion_ErrorsOnNonNumericGPUMilliField(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,0,\n")
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-0000,1000,1024,1,not-a-number,,LS,Running,0,100,10\n")

	_, _, err := BuildInputVersion(nodesCSV, podsCSV, 1, 1.0, 1.0)
	if err == nil {
		t.Fatal("BuildInputVersion returned no error for a non-numeric gpu_milli")
	}
}

// 数値であるべき列が数字でないノードの行は、エラーで返す。
func TestBuildInputVersion_ErrorsOnNonNumericNodeField(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,not-a-number,262144,0,\n")
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-0000,1000,1024,0,0,,LS,Running,0,100,10\n")

	_, _, err := BuildInputVersion(nodesCSV, podsCSV, 1, 1.0, 1.0)
	if err == nil {
		t.Fatal("BuildInputVersion returned no error for a non-numeric node cpu_milli")
	}
}

// ===== FilterGPUNodes =====

// FilterGPUNodes は、gpu が0以下の行を落とし、gpu が1以上の行だけを残す。
func TestFilterGPUNodes_KeepsOnlyNodesWithGPUs(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader +
		"openb-node-cpu-only,32000,262144,0,\n" +
		"openb-node-gpu,32000,262144,2,V100M16\n")

	filtered, err := FilterGPUNodes(nodesCSV)
	if err != nil {
		t.Fatalf("FilterGPUNodes returned error: %v", err)
	}

	records, err := parseNodeCSVRecords(filtered)
	if err != nil {
		t.Fatalf("parseNodeCSVRecords could not read the filtered CSV: %v", err)
	}
	if len(records) != 1 || records[0].name != "openb-node-gpu" {
		t.Fatalf("filtered records = %+v, want only openb-node-gpu", records)
	}
}

// 数値であるべき列が数字でないノードの行は、FilterGPUNodes もエラーで返す。
func TestFilterGPUNodes_ErrorsOnNonNumericField(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,not-a-number,262144,1,\n")

	_, err := FilterGPUNodes(nodesCSV)
	if err == nil {
		t.Fatal("FilterGPUNodes returned no error for a non-numeric cpu_milli")
	}
}

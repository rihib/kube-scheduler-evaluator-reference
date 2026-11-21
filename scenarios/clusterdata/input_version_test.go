package clusterdata

import (
	"fmt"
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

func isReplicaSetObject(o any) bool {
	_, ok := o.(*appsv1.ReplicaSet)
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

// 入力の版は、ノードの CSV にある行をすべて使う。
func TestBuildInputVersion_UsesAllNodes(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader +
		"openb-node-0000,32000,262144,0,\n" +
		"openb-node-0001,32000,262144,0,\n")
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-0000,1000,1024,0,0,,LS,Running,0,100,10\n")

	events, summary, err := BuildInputVersion(nodesCSV, podsCSV, 1, 1.0, 1.0)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}

	nodeCreates := countEventsByType(events, definition.EventTypeCreate, isNodeObject)
	if nodeCreates != 2 {
		t.Errorf("node create events = %d, want 2", nodeCreates)
	}
	if summary.UsedPodCount != 1 {
		t.Errorf("UsedPodCount = %d, want 1", summary.UsedPodCount)
	}
}

// pod_phase が Pending の Pod は入力の版から除く。
func TestBuildInputVersion_ExcludesPendingPods(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,0,\n")
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-pending,1000,1024,0,0,,LS,Pending,0,100,10\n" +
		"openb-pod-running,1000,1024,0,0,,LS,Running,0,100,10\n")

	events, summary, err := BuildInputVersion(nodesCSV, podsCSV, 1, 1.0, 1.0)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}

	names := jobCreateNames(events)
	if len(names) != 1 || names[0] != "openb-pod-running" {
		t.Errorf("job create names = %v, want [openb-pod-running]", names)
	}
	if summary.UsedPodCount != 1 {
		t.Errorf("UsedPodCount = %d, want 1", summary.UsedPodCount)
	}
	if summary.ExcludedPodCount != 1 {
		t.Errorf("ExcludedPodCount = %d, want 1", summary.ExcludedPodCount)
	}
}

// pod_phase が Failed の Pod は、ほかの Pod と同じく入力の版に残り、実行時間が
// 経ったら完了する対象として数える。
func TestBuildInputVersion_KeepsFailedPods(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,0,\n")
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-failed,1000,1024,0,0,,LS,Failed,0,100,10\n" +
		"openb-pod-running,1000,1024,0,0,,LS,Running,0,100,10\n")

	events, summary, err := BuildInputVersion(nodesCSV, podsCSV, 1, 1.0, 1.0)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}

	names := jobCreateNames(events)
	if len(names) != 2 {
		t.Errorf("job create names = %v, want 2 names (failed pod kept)", names)
	}
	if summary.UsedPodCount != 2 {
		t.Errorf("UsedPodCount = %d, want 2", summary.UsedPodCount)
	}
	if summary.FailedPodCount != 1 {
		t.Errorf("FailedPodCount = %d, want 1", summary.FailedPodCount)
	}
}

// 実行時間は、deletion_time から scheduled_time を引いた値にする。
func TestBuildInputVersion_ExecutionDurationIsDeletionMinusScheduled(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,0,\n")
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-0000,1000,1024,0,0,,LS,Running,0,100,10\n")

	events, _, err := BuildInputVersion(nodesCSV, podsCSV, 1, 1.0, 1.0)
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

// gpu_spec が空でない Pod には、GPU の型のラベルが指定された型のどれかに
// 一致するノードにだけ置けるよう nodeAffinity を付ける。
func TestBuildInputVersion_AddsNodeAffinityFromGPUSpec(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,0,\n")
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-0000,1000,1024,1,1000,V100M16|V100M32,LS,Running,0,100,10\n")

	events, _, err := BuildInputVersion(nodesCSV, podsCSV, 1, 1.0, 1.0)
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
		"openb-pod-0000,1000,1024,0,0,,LS,Running,0,100,10\n")

	events, _, err := BuildInputVersion(nodesCSV, podsCSV, 1, 1.0, 1.0)
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

func tenEligiblePodsCSV() *strings.Reader {
	var b strings.Builder
	b.WriteString(podsCSVHeader)
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&b, "openb-pod-%04d,1000,1024,0,0,,LS,Running,%d,%d,%d\n", i, i*10, i*10+100, i*10+10)
	}
	return strings.NewReader(b.String())
}

// 入力の版は、シードから Pending を除いた Pod の80%を抜き出す。
func TestBuildInputVersion_SamplesEightyPercentOfEligiblePods(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,0,\n")

	_, summary, err := BuildInputVersion(nodesCSV, tenEligiblePodsCSV(), 1, 1.0, 0.8)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}

	if summary.UsedPodCount != 8 {
		t.Errorf("UsedPodCount = %d, want 8", summary.UsedPodCount)
	}
	if summary.ExcludedPodCount != 2 {
		t.Errorf("ExcludedPodCount = %d, want 2", summary.ExcludedPodCount)
	}
}

// 同じシードなら、抜き出される Pod の集合と順が同じになる。
func TestBuildInputVersion_SameSeedProducesSameSample(t *testing.T) {
	nodesCSV1 := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,0,\n")
	nodesCSV2 := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,0,\n")

	events1, _, err := BuildInputVersion(nodesCSV1, tenEligiblePodsCSV(), 42, 1.0, 0.8)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}
	events2, _, err := BuildInputVersion(nodesCSV2, tenEligiblePodsCSV(), 42, 1.0, 0.8)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}

	names1 := jobCreateNames(events1)
	names2 := jobCreateNames(events2)
	if !slices.Equal(names1, names2) {
		t.Errorf("same seed produced different samples: %v vs %v", names1, names2)
	}
}

func sixPodsAtTheSameCreationTimeCSV() *strings.Reader {
	var b strings.Builder
	b.WriteString(podsCSVHeader)
	for i := 0; i < 6; i++ {
		fmt.Fprintf(&b, "openb-pod-%04d,1000,1024,0,0,,LS,Running,0,100,10\n", i)
	}
	return strings.NewReader(b.String())
}

// 同じ creation_time の Pod は、間隔0で同じ時刻に投入されるが、シードで
// 投入の順が並べ替えられる。
func TestBuildInputVersion_ShufflesOrderOfSameCreationTime(t *testing.T) {
	nodesCSV1 := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,0,\n")
	nodesCSV2 := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,0,\n")
	nodesCSV3 := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,0,\n")

	eventsSeed1a, _, err := BuildInputVersion(nodesCSV1, sixPodsAtTheSameCreationTimeCSV(), 1, 1.0, 1.0)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}
	eventsSeed1b, _, err := BuildInputVersion(nodesCSV3, sixPodsAtTheSameCreationTimeCSV(), 1, 1.0, 1.0)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}
	eventsSeed2, _, err := BuildInputVersion(nodesCSV2, sixPodsAtTheSameCreationTimeCSV(), 2, 1.0, 1.0)
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

// Pod は creation_time の間隔で投入される。この CSV の ρ はちょうど
// 0.9375（0.9375は 1000*1000*3 / (32000*100) の cpu の ρ）になるので、
// targetRho に同じ値を渡せば倍率が1になり、間隔はそのまま creation_time
// の差になる。
func TestBuildInputVersion_SubmissionIntervalIsCreationTimeGap(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,0,\n")
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-0000,1000,1024,0,0,,LS,Running,0,1000,0\n" +
		"openb-pod-0001,1000,1024,0,0,,LS,Running,30,1030,30\n" +
		"openb-pod-0002,1000,1024,0,0,,LS,Running,100,1100,100\n")

	events, _, err := BuildInputVersion(nodesCSV, podsCSV, 1, 0.9375, 1.0)
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
	if intervals[0] != 30*time.Second {
		t.Errorf("intervals[0] = %v, want 30s", intervals[0])
	}
	if intervals[1] != 70*time.Second {
		t.Errorf("intervals[1] = %v, want 70s", intervals[1])
	}
}

func approxEqual(a, b, epsilon float64) bool {
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	return diff <= epsilon
}

// ρ は、資源ごとの要求量×実行時間の合計を容量の合計×投入期間で割った値の
// 最大値である。倍率をかけた後の ρ は、目標の ρ に一致する。
func TestBuildInputVersion_RhoBeforeAndAfterScaling(t *testing.T) {
	// 1ノード（cpu=32000m, mem=262144Mi）、3つのPod（各cpu=1000m,
	// mem=1024Mi、実行時間1000s）、creation_timeは0, 30, 100（投入期間
	// 100s）。手で求めたcpuのρ = 1000*1000*3 / (32000*100) = 0.9375、
	// memのρ = 1024*1000*3 / (262144*100) ≈ 0.01172 なので、最大は
	// cpuの0.9375になる。
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,0,\n")
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-0000,1000,1024,0,0,,LS,Running,0,1000,0\n" +
		"openb-pod-0001,1000,1024,0,0,,LS,Running,30,1030,30\n" +
		"openb-pod-0002,1000,1024,0,0,,LS,Running,100,1100,100\n")

	_, summary, err := BuildInputVersion(nodesCSV, podsCSV, 1, 1.1, 1.0)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}

	if !approxEqual(summary.RhoBeforeScaling, 0.9375, 1e-9) {
		t.Errorf("RhoBeforeScaling = %v, want 0.9375", summary.RhoBeforeScaling)
	}
	if !approxEqual(summary.RhoAfterScaling, 1.1, 1e-9) {
		t.Errorf("RhoAfterScaling = %v, want 1.1", summary.RhoAfterScaling)
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

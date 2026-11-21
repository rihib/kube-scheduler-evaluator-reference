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
		"openb-pod-0000,1000,1024,0,0,,LS,Running,0,100,10\n" +
		"openb-pod-0001,1000,1024,0,0,,LS,Running,50,150,60\n")

	events, summary, err := BuildInputVersion(nodesCSV, podsCSV, 1, 1.0, 1.0)
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

	events, summary, err := BuildInputVersion(nodesCSV, podsCSV, 1, 1.0, 1.0)
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
		"openb-pod-running,1000,1024,0,0,,LS,Running,0,100,10\n" +
		"openb-pod-running2,1000,1024,0,0,,LS,Running,50,150,60\n")

	events, summary, err := BuildInputVersion(nodesCSV, podsCSV, 1, 1.0, 1.0)
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

// GPU を要求する Pod には gpu-milli 注釈にGPU1枚あたりの既定値（1000）を
// 入れる。evaluator はこの注釈を num_gpu×1000 の既定より優先するので、0の
// ままだと GPU を要求する Pod すべての GPU 利用率が記録上0になってしまう。
func TestBuildInputVersion_GPUMilliAnnotationIsDefaultPerGPU(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,2,V100M16\n")
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-0000,1000,1024,1,1000,,LS,Running,0,100,10\n" +
		"openb-pod-0001,1000,1024,0,0,,LS,Running,50,150,60\n")

	events, _, err := BuildInputVersion(nodesCSV, podsCSV, 1, 1.0, 1.0)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}

	job := jobCreateByName(events, "openb-pod-0000")
	if job == nil {
		t.Fatalf("no create event for openb-pod-0000")
	}
	key := fmt.Sprintf("%s/gpu-milli", consts.UserID)
	got := job.Spec.Template.Annotations[key]
	if got != "1000" {
		t.Errorf("gpu-milli annotation = %q, want %q", got, "1000")
	}
}

// gpu_spec が空でない Pod には、GPU の型のラベルが指定された型のどれかに
// 一致するノードにだけ置けるよう nodeAffinity を付ける。
func TestBuildInputVersion_AddsNodeAffinityFromGPUSpec(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,0,\n")
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-0000,1000,1024,1,1000,V100M16|V100M32,LS,Running,0,100,10\n" +
		"openb-pod-0001,1000,1024,0,0,,LS,Running,50,150,60\n")

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
		"openb-pod-0000,1000,1024,0,0,,LS,Running,0,100,10\n" +
		"openb-pod-0001,1000,1024,0,0,,LS,Running,50,150,60\n")

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

// sixPodsAtTheSameCreationTimeCSV returns six pods sharing creation_time 0,
// plus one anchor pod far away in time so the submission period is not
// zero (BuildInputVersion requires that to compute rho). The anchor's
// creation_time never collides with the group of six, so it does not
// affect which of the six pods tie for the same submission time.
func sixPodsAtTheSameCreationTimeCSV() *strings.Reader {
	var b strings.Builder
	b.WriteString(podsCSVHeader)
	for i := 0; i < 6; i++ {
		fmt.Fprintf(&b, "openb-pod-%04d,1000,1024,0,0,,LS,Running,0,100,10\n", i)
	}
	b.WriteString("openb-pod-anchor,1000,1024,0,0,,LS,Running,1000000,1000100,1000010\n")
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

// Pod は creation_time の間隔で投入される。evaluator はイベントに付けた
// interval をそのイベント自身の発火の前に足す（イベント自身の待ち時間であり、
// 次のイベントまでの待ち時間ではない）ので、最初の Pod の interval は0、
// それ以降は直前の Pod との creation_time の差になる。この CSV の ρ はちょうど
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

// openb の実データでは、Pending の Pod は scheduled_time が空になる。
// Pending は入力の版から除くので、このフィールドが空でも失敗しない。
func TestBuildInputVersion_PendingPodWithEmptyScheduledTimeDoesNotError(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,0,\n")
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-pending,1000,1024,0,0,,LS,Pending,0,100,\n" +
		"openb-pod-running,1000,1024,0,0,,LS,Running,0,100,10\n" +
		"openb-pod-running2,1000,1024,0,0,,LS,Running,50,150,60\n")

	_, summary, err := BuildInputVersion(nodesCSV, podsCSV, 1, 1.0, 1.0)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}
	if summary.UsedPodCount != 2 {
		t.Errorf("UsedPodCount = %d, want 2", summary.UsedPodCount)
	}
}

// sampleRatio が0〜1の範囲を外れると、不正な入力としてエラーを返す。
func TestBuildInputVersion_ErrorsOnSampleRatioOutOfRange(t *testing.T) {
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

// sampleRatio=0 は使う Pod が0件になる境界だが、これは「負荷のない入力」
// として正当なのでエラーにはせず、件数とρがすべて0になる。
func TestBuildInputVersion_SampleRatioZeroUsesNoPods(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,0,\n")
	podsCSV := tenEligiblePodsCSV()

	events, summary, err := BuildInputVersion(nodesCSV, podsCSV, 1, 1.0, 0.0)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}

	if summary.UsedPodCount != 0 {
		t.Errorf("UsedPodCount = %d, want 0", summary.UsedPodCount)
	}
	if len(jobCreateNames(events)) != 0 {
		t.Errorf("job create events = %v, want none", jobCreateNames(events))
	}
	if summary.RhoBeforeScaling != 0 {
		t.Errorf("RhoBeforeScaling = %v, want 0", summary.RhoBeforeScaling)
	}
	if summary.RhoAfterScaling != 0 {
		t.Errorf("RhoAfterScaling = %v, want 0", summary.RhoAfterScaling)
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

// buildInputVersionJob が作る Job の資源の要求は、CSV の値どおりになる。
func TestBuildInputVersion_JobResourceRequestsMatchCSV(t *testing.T) {
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,2,\n")
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-0000,1234,5678,3,0,,LS,Running,0,100,10\n" +
		"openb-pod-0001,1000,1024,0,0,,LS,Running,50,150,60\n")

	events, _, err := BuildInputVersion(nodesCSV, podsCSV, 1, 1.0, 1.0)
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
	gpu := requests[corev1.ResourceName("nvidia.com/gpu")]
	if got := gpu.Value(); got != 3 {
		t.Errorf("gpu request = %d, want 3", got)
	}
}

// ρ は資源ごとの比の最大値なので、GPU の比が cpu・メモリより大きいときは
// GPU の比になる。
func TestBuildInputVersion_RhoIsDrivenByGPUWhenItIsTheBottleneck(t *testing.T) {
	// ノードはcpu=32000m, mem=262144Mi, gpu=2。2つのPod（各cpu=100m,
	// mem=100Mi, num_gpu=2、実行時間1000s、creation_timeは0と100で投入
	// 期間100s）だと、cpuのρ=100*1000*2/(32000*100)=0.0625、memのρ=
	// 100*1000*2/(262144*100)≈0.000763、gpuのρ=2*1000*2/(2*100)=20.0に
	// なり、最大はgpuになる。
	nodesCSV := strings.NewReader(nodesCSVHeader + "openb-node-0000,32000,262144,2,\n")
	podsCSV := strings.NewReader(podsCSVHeader +
		"openb-pod-0000,100,100,2,0,,LS,Running,0,1000,0\n" +
		"openb-pod-0001,100,100,2,0,,LS,Running,100,1100,100\n")

	_, summary, err := BuildInputVersion(nodesCSV, podsCSV, 1, 20.0, 1.0)
	if err != nil {
		t.Fatalf("BuildInputVersion returned error: %v", err)
	}
	if !approxEqual(summary.RhoBeforeScaling, 20.0, 1e-9) {
		t.Errorf("RhoBeforeScaling = %v, want 20.0", summary.RhoBeforeScaling)
	}
}

// ρ を求める容量は、すべてのノードの容量の合計である。
func TestBuildInputVersion_RhoUsesCapacitySummedAcrossNodes(t *testing.T) {
	// 2ノード（各cpu=16000m, mem=131072Mi）の合計はcpu=32000m,
	// mem=262144Miで、TestBuildInputVersion_RhoBeforeAndAfterScalingの
	// 1ノード版と同じ容量になる。同じPodを使えば、同じρ=0.9375になる。
	nodesCSV := strings.NewReader(nodesCSVHeader +
		"openb-node-0000,16000,131072,0,\n" +
		"openb-node-0001,16000,131072,0,\n")
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

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"testing"

	"github.com/pfnet/kube-scheduler-evaluator/cmd/evaluator"
	schedulerconfig "k8s.io/kubernetes/pkg/scheduler/apis/config"
)

// --rhos はカンマ区切りの目標ρの一覧を、前後の空白を落として float64 の列にする。
func TestParseRhos_SplitsCommaSeparatedValues(t *testing.T) {
	got, err := parseRhos("0.5, 1.1")
	if err != nil {
		t.Fatalf("parseRhos: %v", err)
	}
	want := []float64{0.5, 1.1}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("parseRhos(\"0.5, 1.1\") = %v, want %v", got, want)
	}
}

// 数値として読めない値が混じっていればエラーにする。
func TestParseRhos_FailsOnInvalidValue(t *testing.T) {
	if _, err := parseRhos("0.5,not-a-number"); err == nil {
		t.Fatalf("parseRhos: expected an error, got nil")
	}
}

// strconv.ParseFloat は "+Inf" を読めてしまうが、+Inf はscenarioIDを経て
// definition.NewScenario のIDの正規表現チェックで panic するので、境界で
// 弾く。
func TestParseRhos_FailsOnInfinity(t *testing.T) {
	if _, err := parseRhos("0.5,+Inf"); err == nil {
		t.Fatalf("parseRhos: expected an error, got nil")
	}
}

// シナリオIDは、ρと profile の組ごとに違う値になり、小数点を含まない
// （definition.NewScenario が要求する ^[a-zA-Z][a-zA-Z0-9-]*$ を満たすため）。
func TestScenarioID_DiffersByRhoAndProfile(t *testing.T) {
	ids := map[string]bool{}
	for _, id := range []string{
		scenarioID(0.5, schedulerconfig.LeastAllocated),
		scenarioID(0.5, schedulerconfig.MostAllocated),
		scenarioID(1.1, schedulerconfig.LeastAllocated),
		scenarioID(1.1, schedulerconfig.MostAllocated),
	} {
		if strings.Contains(id, ".") {
			t.Errorf("scenarioID = %q, must not contain a dot", id)
		}
		if ids[id] {
			t.Errorf("scenarioID = %q, got the same id for a different combo", id)
		}
		ids[id] = true
	}
	if len(ids) != 4 {
		t.Errorf("distinct scenario ids = %d, want 4: %v", len(ids), ids)
	}
}

// ビルド情報に vcs.revision があれば、それを reference のコミットとして使う。
func TestReferenceCommitFrom_UsesVCSRevisionWhenPresent(t *testing.T) {
	commit, found := referenceCommitFrom(&debug.BuildInfo{
		Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "abc123"}},
	})
	if !found || commit != "abc123" {
		t.Errorf("got (%q, %v), want (\"abc123\", true)", commit, found)
	}
}

// vcs.revision が無ければ（go test のバイナリなど）、呼び出し元が git に
// フォールバックできるよう found = false を返す。
func TestReferenceCommitFrom_ReportsNotFoundWithoutVCSRevision(t *testing.T) {
	commit, found := referenceCommitFrom(&debug.BuildInfo{})
	if found {
		t.Errorf("got (%q, true), want found = false so the caller falls back to git", commit)
	}
}

// vcs.modified が true なら、汚れた作業ツリーから作られたバイナリだと分かる
// よう、コミットに "-dirty" を付ける。
func TestReferenceCommitFrom_MarksDirtyWhenVCSModified(t *testing.T) {
	commit, found := referenceCommitFrom(&debug.BuildInfo{
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "abc123"},
			{Key: "vcs.modified", Value: "true"},
		},
	})
	if !found || commit != "abc123-dirty" {
		t.Errorf("got (%q, %v), want (\"abc123-dirty\", true)", commit, found)
	}
}

const pilotTestNodesCSV = "sn,cpu_milli,memory_mib,gpu,model\n" +
	"openb-node-0000,32000,262144,0,\n"

// The pods CSV's file name (minus ".csv") becomes definition.RunIdentity.TraceVersion
// (main.go's traceVersion), which must start with traceName ("openb"); the
// file is therefore named openb_pod_list_test.csv rather than pods.csv.
const pilotTestPodsFile = "openb_pod_list_test.csv"

const pilotTestPodsCSV = "name,cpu_milli,memory_mib,num_gpu,gpu_milli,gpu_spec,qos,pod_phase,creation_time,deletion_time,scheduled_time\n" +
	"openb-pod-0000,1000,1024,0,0,,LS,Running,0,1000,0\n" +
	"openb-pod-0001,1000,1024,0,0,,LS,Running,30,1030,30\n" +
	"openb-pod-0002,1000,1024,0,0,,LS,Running,100,1100,100\n"

func writeTempCSV(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write %s: %v", path, err)
	}
	return path
}

// fakeRecordStore stands in for where runs' records are written
// (units/U03.md: "記録の書き出し先と reference のコミットに Fake を渡す")。
// It keeps each run's header (its first WriteLine call) as parsed JSON and
// counts the transitions written after it, so a test can tell runs apart by
// what combination they belong to and notice a run that recorded no
// transitions at all.
type fakeRecordStore struct {
	mu   sync.Mutex
	runs []*fakeRun
}

// fakeRun is one run's header, how many transition lines followed it, and
// which nodes pods actually got assigned to (internal/record.Transition's
// operation == "assign_node" && state == "assigned_node" rows, keyed by
// their "after" value). assignedNodes only records how many distinct nodes
// were used, never which ones, since kube-scheduler breaks ties between
// equally-scored nodes at random (testing.md: "配置先のノード名を確かめない")。
type fakeRun struct {
	header          map[string]any
	transitionLines int
	assignedNodes   map[string]bool
}

func (s *fakeRecordStore) factory(evaluator.RecordRunInfo) (evaluator.RecordWriter, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := &fakeRun{assignedNodes: map[string]bool{}}
	s.runs = append(s.runs, run)
	return &fakeRecordWriter{run: run}, nil
}

type fakeRecordWriter struct {
	run         *fakeRun
	wroteHeader bool
}

func (w *fakeRecordWriter) WriteLine(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	if !w.wroteHeader {
		w.wroteHeader = true
		w.run.header = m
		return nil
	}
	w.run.transitionLines++
	if m["operation"] == "assign_node" && m["state"] == "assigned_node" {
		if after, ok := m["after"].(string); ok && after != "" {
			w.run.assignedNodes[after] = true
		}
	}
	return nil
}

func (w *fakeRecordWriter) Close() error { return nil }

// comboKey labels a header by its (rho, scheduler profile) combination,
// collapsing the profile's full description down to which of the two
// pilotStrategies it names.
func comboKey(t *testing.T, header map[string]any) string {
	t.Helper()
	input, _ := header["input"].(map[string]any)
	sched, _ := header["scheduler"].(map[string]any)
	if input == nil || sched == nil {
		t.Fatalf("header is missing input or scheduler: %v", header)
	}
	profile, _ := sched["profile"].(string)
	return fmt.Sprintf("%v-%s", input["rho"], profileLabel(t, profile))
}

// profileLabel picks out which of the two pilot profiles a header's
// scheduler.profile description belongs to, by substring (units/U03.md's
// description is a full sentence, not a bare name).
func profileLabel(t *testing.T, description string) string {
	t.Helper()
	switch {
	case strings.Contains(description, "LeastAllocated"):
		return "LeastAllocated"
	case strings.Contains(description, "MostAllocated"):
		return "MostAllocated"
	default:
		t.Fatalf("scheduler.profile %q names neither LeastAllocated nor MostAllocated", description)
		return ""
	}
}

// run(ρ 0.5/1.1, 2つの profile, 2反復) は、組ごとに反復の回数だけ記録の
// 実行ができ、1行目のinput.rho・scheduler.profileで組が区別でき、
// input.reference_commit・input.trace_name・input.trace_version・
// input.input_seedにも正しい値が入り、どの実行も遷移を1件以上記録する。
func TestRun_WritesOneRunPerComboAndIteration(t *testing.T) {
	nodesPath := writeTempCSV(t, "nodes.csv", pilotTestNodesCSV)
	podsPath := writeTempCSV(t, pilotTestPodsFile, pilotTestPodsCSV)
	store := &fakeRecordStore{}

	var stderr bytes.Buffer
	code := run([]string{
		"--nodes=" + nodesPath,
		"--pods=" + podsPath,
		"--seed=1",
		"--ratio=1.0",
		"--rhos=0.5,1.1",
		"--iterations=2",
	}, &stderr, store.factory, func() (string, error) { return "fake-reference-commit", nil })
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %s)", code, stderr.String())
	}

	if len(store.runs) != 8 { // 2 rhos x 2 profiles x 2 iterations
		t.Fatalf("run count = %d, want 8", len(store.runs))
	}

	comboCounts := map[string]int{}
	var transitionLineCounts []int
	wantTraceVersion := strings.TrimSuffix(pilotTestPodsFile, ".csv")
	for _, run := range store.runs {
		h := run.header
		input, _ := h["input"].(map[string]any)
		if got, _ := input["reference_commit"].(string); got != "fake-reference-commit" {
			t.Errorf("input.reference_commit = %q, want %q", got, "fake-reference-commit")
		}
		if got, _ := input["trace_name"].(string); got != traceName {
			t.Errorf("input.trace_name = %q, want %q", got, traceName)
		}
		if got, _ := input["trace_version"].(string); got != wantTraceVersion {
			t.Errorf("input.trace_version = %q, want %q", got, wantTraceVersion)
		}
		if got, _ := input["input_seed"].(string); got != "1" {
			t.Errorf("input.input_seed = %q, want %q", got, "1")
		}
		if run.transitionLines == 0 {
			t.Errorf("run with header %v recorded no transitions", h)
		}
		transitionLineCounts = append(transitionLineCounts, run.transitionLines)
		comboCounts[comboKey(t, h)]++
	}
	for _, combo := range []string{"0.5-LeastAllocated", "0.5-MostAllocated", "1.1-LeastAllocated", "1.1-MostAllocated"} {
		if comboCounts[combo] != 2 {
			t.Errorf("combo %s ran %d times, want 2 (comboCounts: %v)", combo, comboCounts[combo], comboCounts)
		}
	}
	// All 8 runs replay the same 3-pod event slice onto the same single
	// node, so they must all record the same number of transitions; a run
	// recording fewer (e.g. a later iteration silently not being sent the
	// events) would otherwise go unnoticed as long as its count stayed
	// above zero.
	for _, n := range transitionLineCounts[1:] {
		if n != transitionLineCounts[0] {
			t.Errorf("transition line counts differ across runs: %v, want all equal", transitionLineCounts)
			break
		}
	}
}

// pilotSpreadNodesCSV has two equal-capacity nodes, each able to host all
// four pods in pilotSpreadPodsCSV on its own (4*500 = 2000 <= 2200), so
// LeastAllocated and MostAllocated disagree on how many nodes get used.
const pilotSpreadNodesCSV = "sn,cpu_milli,memory_mib,gpu,model\n" +
	"openb-node-0000,2200,2200,0,\n" +
	"openb-node-0001,2200,2200,0,\n"

// pilotSpreadPodsCSV's four pods all request a quarter of one node's
// capacity and stay running throughout (deletion_time far past every
// creation_time), so all four coexist and must all be scheduled.
const pilotSpreadPodsCSV = "name,cpu_milli,memory_mib,num_gpu,gpu_milli,gpu_spec,qos,pod_phase,creation_time,deletion_time,scheduled_time\n" +
	"openb-pod-0000,500,500,0,0,,LS,Running,0,10000,0\n" +
	"openb-pod-0001,500,500,0,0,,LS,Running,10,10000,10\n" +
	"openb-pod-0002,500,500,0,0,,LS,Running,20,10000,20\n" +
	"openb-pod-0003,500,500,0,0,,LS,Running,30,10000,30\n"

// scheduler.WithProfiles(schedulerProfile) が実際にシナリオに渡っていなければ
// （既定の scheduler のまま動けば）、LeastAllocated と MostAllocated の実行の
// 置かれたノードの数に違いが出ない。ノードの数字は資源の使用率を均そうとする
// LeastAllocated が2ノードに分け、使用率を高くまとめようとする
// MostAllocated が1ノードに詰めることを利用する（配置先のノード名そのものは
// 乱数で選ばれるので確かめない）。
func TestRun_SchedulerProfileAffectsNodePlacement(t *testing.T) {
	nodesPath := writeTempCSV(t, "nodes.csv", pilotSpreadNodesCSV)
	podsPath := writeTempCSV(t, pilotTestPodsFile, pilotSpreadPodsCSV)
	store := &fakeRecordStore{}

	var stderr bytes.Buffer
	code := run([]string{
		"--nodes=" + nodesPath,
		"--pods=" + podsPath,
		"--seed=1",
		"--ratio=1.0",
		"--rhos=0.5",
		"--iterations=1",
	}, &stderr, store.factory, func() (string, error) { return "fake-reference-commit", nil })
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %s)", code, stderr.String())
	}
	if len(store.runs) != 2 { // 1 rho x 2 profiles x 1 iteration
		t.Fatalf("run count = %d, want 2", len(store.runs))
	}

	for _, run := range store.runs {
		sched, _ := run.header["scheduler"].(map[string]any)
		profile, _ := sched["profile"].(string)
		label := profileLabel(t, profile)
		nodesUsed := len(run.assignedNodes)
		switch label {
		case "LeastAllocated":
			if nodesUsed != 2 {
				t.Errorf("LeastAllocated used %d node(s), want 2 (spread across both nodes)", nodesUsed)
			}
		case "MostAllocated":
			if nodesUsed != 1 {
				t.Errorf("MostAllocated used %d node(s), want 1 (packed onto one node)", nodesUsed)
			}
		}
	}
}

// pilotGPUSpreadNodesCSV has two equal-capacity GPU nodes. cpu/memory
// capacity is generously larger than any pod could ever request here, so
// every pod's cpu and memory allocation ratio stays at 0 on both nodes no
// matter where it lands; only gpu usage differs by placement, isolating
// nvidia.com/gpu's contribution to NodeResourcesFit's score from cpu/memory.
const pilotGPUSpreadNodesCSV = "sn,cpu_milli,memory_mib,gpu,model\n" +
	"openb-node-0000,8000,8192,4,\n" +
	"openb-node-0001,8000,8192,4,\n"

// pilotGPUSpreadPodsCSV's four pods each request one GPU and no cpu/memory,
// and stay running throughout (deletion_time far past every creation_time),
// so all four coexist and must all be scheduled, together using exactly one
// node's worth of GPUs (4*1 = 4).
const pilotGPUSpreadPodsCSV = "name,cpu_milli,memory_mib,num_gpu,gpu_milli,gpu_spec,qos,pod_phase,creation_time,deletion_time,scheduled_time\n" +
	"openb-pod-0000,0,0,1,0,,LS,Running,0,10000,0\n" +
	"openb-pod-0001,0,0,1,0,,LS,Running,10,10000,10\n" +
	"openb-pod-0002,0,0,1,0,,LS,Running,20,10000,20\n" +
	"openb-pod-0003,0,0,1,0,,LS,Running,30,10000,30\n"

// schedulerprofile.Pilot が NodeResourcesFit の ScoringStrategy に
// nvidia.com/gpu を資源として入れていなければ、cpu も memory も使わない
// このシナリオでは両ノードの採点が常に同点になり、LeastAllocated と
// MostAllocated のどちらも置かれたノードの数に違いが出ない。GPU の資源が
// 実際に採点へ効いていることを、置かれたノードの数（名前は見ない）で
// 確かめる。main_test.go の TestRun_SchedulerProfileAffectsNodePlacement が
// cpu/memory について確かめているのと対になる。
func TestRun_SchedulerProfileAffectsGPUNodePlacement(t *testing.T) {
	nodesPath := writeTempCSV(t, "nodes.csv", pilotGPUSpreadNodesCSV)
	podsPath := writeTempCSV(t, pilotTestPodsFile, pilotGPUSpreadPodsCSV)
	store := &fakeRecordStore{}

	var stderr bytes.Buffer
	code := run([]string{
		"--nodes=" + nodesPath,
		"--pods=" + podsPath,
		"--seed=1",
		"--ratio=1.0",
		"--rhos=0.5",
		"--iterations=1",
	}, &stderr, store.factory, func() (string, error) { return "fake-reference-commit", nil })
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %s)", code, stderr.String())
	}
	if len(store.runs) != 2 { // 1 rho x 2 profiles x 1 iteration
		t.Fatalf("run count = %d, want 2", len(store.runs))
	}

	for _, run := range store.runs {
		sched, _ := run.header["scheduler"].(map[string]any)
		profile, _ := sched["profile"].(string)
		label := profileLabel(t, profile)
		nodesUsed := len(run.assignedNodes)
		switch label {
		case "LeastAllocated":
			if nodesUsed != 2 {
				t.Errorf("LeastAllocated used %d node(s), want 2 (spread GPU usage across both nodes)", nodesUsed)
			}
		case "MostAllocated":
			if nodesUsed != 1 {
				t.Errorf("MostAllocated used %d node(s), want 1 (packed GPU usage onto one node)", nodesUsed)
			}
		}
	}
}

// --iterations が0以下なら、記録が1つもできない実行を終了コード0で終わらせず
// 失敗として終わる。
func TestRun_FailsWhenIterationsIsNotPositive(t *testing.T) {
	nodesPath := writeTempCSV(t, "nodes.csv", pilotTestNodesCSV)
	podsPath := writeTempCSV(t, pilotTestPodsFile, pilotTestPodsCSV)
	store := &fakeRecordStore{}

	var stderr bytes.Buffer
	code := run([]string{
		"--nodes=" + nodesPath,
		"--pods=" + podsPath,
		"--iterations=0",
	}, &stderr, store.factory, func() (string, error) { return "fake-reference-commit", nil })
	if code == 0 {
		t.Fatalf("exit code = 0, want nonzero")
	}
	if len(store.runs) != 0 {
		t.Errorf("run count = %d, want 0", len(store.runs))
	}
}

// --pods のファイル名がopenbのトレース名で始まらなければ、記録の1行目が
// 入力のトレースについて事実でないことを名乗る前に失敗として終わる。
func TestRun_FailsWhenPodsFileNameDoesNotNameOpenbTrace(t *testing.T) {
	nodesPath := writeTempCSV(t, "nodes.csv", pilotTestNodesCSV)
	podsPath := writeTempCSV(t, "some_other_trace_pods.csv", pilotTestPodsCSV)
	store := &fakeRecordStore{}

	var stderr bytes.Buffer
	code := run([]string{
		"--nodes=" + nodesPath,
		"--pods=" + podsPath,
	}, &stderr, store.factory, func() (string, error) { return "fake-reference-commit", nil })
	if code == 0 {
		t.Fatalf("exit code = 0, want nonzero")
	}
	if len(store.runs) != 0 {
		t.Errorf("run count = %d, want 0", len(store.runs))
	}
}

// reference のコミットが取れないとき（例: ビルド情報もgitも使えない環境）は、
// 実行を1つも始めずに失敗として終わる。
func TestRun_FailsWhenReferenceCommitProviderFails(t *testing.T) {
	nodesPath := writeTempCSV(t, "nodes.csv", pilotTestNodesCSV)
	podsPath := writeTempCSV(t, pilotTestPodsFile, pilotTestPodsCSV)
	store := &fakeRecordStore{}

	var stderr bytes.Buffer
	code := run([]string{
		"--nodes=" + nodesPath,
		"--pods=" + podsPath,
	}, &stderr, store.factory, func() (string, error) { return "", fmt.Errorf("no commit available") })
	if code == 0 {
		t.Fatalf("exit code = 0, want nonzero")
	}
	if len(store.runs) != 0 {
		t.Errorf("run count = %d, want 0", len(store.runs))
	}
}

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

// シナリオIDは、ρと profile の組ごとに違う値になり、小数点を含まない
// （definition.NewScenario が要求する ^[a-zA-Z][a-zA-Z0-9-]*$ を満たすため）。
func TestScenarioID_DiffersByRhoAndProfile(t *testing.T) {
	least := pilotProfile{name: "LeastAllocated"}
	most := pilotProfile{name: "MostAllocated"}

	ids := map[string]bool{}
	for _, id := range []string{
		scenarioID(0.5, least),
		scenarioID(0.5, most),
		scenarioID(1.1, least),
		scenarioID(1.1, most),
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

const pilotTestNodesCSV = "sn,cpu_milli,memory_mib,gpu,model\n" +
	"openb-node-0000,32000,262144,0,\n"

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
// It keeps each run's header (its first WriteLine call) as parsed JSON, so
// a test can tell runs apart by what combination they belong to.
type fakeRecordStore struct {
	mu      sync.Mutex
	headers []map[string]any
}

func (s *fakeRecordStore) factory(evaluator.RecordRunInfo) (evaluator.RecordWriter, error) {
	return &fakeRecordWriter{store: s}, nil
}

type fakeRecordWriter struct {
	store       *fakeRecordStore
	wroteHeader bool
}

func (w *fakeRecordWriter) WriteLine(v any) error {
	if w.wroteHeader {
		return nil
	}
	w.wroteHeader = true
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	w.store.mu.Lock()
	w.store.headers = append(w.store.headers, m)
	w.store.mu.Unlock()
	return nil
}

func (w *fakeRecordWriter) Close() error { return nil }

// comboKey labels a header by its (rho, scheduler profile) combination,
// collapsing the profile's full description down to which of the two
// pilotProfiles it names.
func comboKey(t *testing.T, header map[string]any) string {
	t.Helper()
	input, _ := header["input"].(map[string]any)
	sched, _ := header["scheduler"].(map[string]any)
	if input == nil || sched == nil {
		t.Fatalf("header is missing input or scheduler: %v", header)
	}
	profile, _ := sched["profile"].(string)
	var label string
	switch {
	case strings.Contains(profile, "LeastAllocated"):
		label = "LeastAllocated"
	case strings.Contains(profile, "MostAllocated"):
		label = "MostAllocated"
	default:
		t.Fatalf("scheduler.profile %q names neither LeastAllocated nor MostAllocated", profile)
	}
	return fmt.Sprintf("%v-%s", input["rho"], label)
}

// run(ρ 0.5/1.1, 2つの profile, 2反復) は、組ごとに反復の回数だけ記録の
// 実行ができ、1行目のinput.rho・scheduler.profileで組が区別でき、
// input.reference_commitにFakeの戻り値が入る。
func TestRun_WritesOneRunPerComboAndIteration(t *testing.T) {
	nodesPath := writeTempCSV(t, "nodes.csv", pilotTestNodesCSV)
	podsPath := writeTempCSV(t, "pods.csv", pilotTestPodsCSV)
	store := &fakeRecordStore{}

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--nodes=" + nodesPath,
		"--pods=" + podsPath,
		"--seed=1",
		"--ratio=1.0",
		"--rhos=0.5,1.1",
		"--iterations=2",
	}, &stdout, &stderr, store.factory, func() (string, error) { return "fake-reference-commit", nil })
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %s)", code, stderr.String())
	}

	if len(store.headers) != 8 { // 2 rhos x 2 profiles x 2 iterations
		t.Fatalf("run count = %d, want 8", len(store.headers))
	}

	comboCounts := map[string]int{}
	for _, h := range store.headers {
		input, _ := h["input"].(map[string]any)
		if got, _ := input["reference_commit"].(string); got != "fake-reference-commit" {
			t.Errorf("input.reference_commit = %q, want %q", got, "fake-reference-commit")
		}
		comboCounts[comboKey(t, h)]++
	}
	for _, combo := range []string{"0.5-LeastAllocated", "0.5-MostAllocated", "1.1-LeastAllocated", "1.1-MostAllocated"} {
		if comboCounts[combo] != 2 {
			t.Errorf("combo %s ran %d times, want 2 (comboCounts: %v)", combo, comboCounts[combo], comboCounts)
		}
	}
}

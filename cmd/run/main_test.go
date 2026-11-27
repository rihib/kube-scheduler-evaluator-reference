package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"testing"

	"github.com/pfnet/kube-scheduler-evaluator/cmd/evaluator"
)

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

// git に問い合わせる経路では、git status --porcelain が空なら、
// git rev-parse HEAD のコミットをそのまま使う。
func TestReferenceCommitFromGit_UsesCommitWhenTreeIsClean(t *testing.T) {
	if got := referenceCommitFromGit("abc123\n", ""); got != "abc123" {
		t.Errorf("got %q, want %q", got, "abc123")
	}
}

// 同じ経路でも、作業ツリーが汚れていれば（git status --porcelain に行があれば）
// "-dirty" を付ける。ビルド情報に vcs.revision が無い形（go run や、
// Makefile の go build）が普段の動かし方なので、この経路が付けないと、
// 汚れたツリーの記録が汚れていないコミットを名乗る。
func TestReferenceCommitFromGit_MarksDirtyWhenTreeIsDirty(t *testing.T) {
	if got := referenceCommitFromGit("abc123\n", " M cmd/run/main.go\n"); got != "abc123-dirty" {
		t.Errorf("got %q, want %q", got, "abc123-dirty")
	}
}

// シナリオIDは、ρごとに違う値になり、小数点を含まない（definition.NewScenario
// が要求する ^[a-zA-Z][a-zA-Z0-9-]*$ を満たすため）。
func TestScenarioID_DiffersByRho(t *testing.T) {
	a, b := scenarioID(0.5), scenarioID(1.1)
	if a == b {
		t.Errorf("scenarioID(0.5) == scenarioID(1.1) == %q, want different ids", a)
	}
	for _, id := range []string{a, b} {
		if strings.Contains(id, ".") {
			t.Errorf("scenarioID = %q, must not contain a dot", id)
		}
	}
}

const runTestNodesCSV = "sn,cpu_milli,memory_mib,gpu,model\n" +
	"openb-node-0000,32000,262144,0,\n"

// pods CSV のファイル名（".csv" を除く）が definition.RunIdentity.TraceVersion
// (main.go の traceVersion) になり、traceName ("openb") で始まる必要がある
// ため、pods.csv ではなく openb_pod_list_test.csv という名前にする。
const runTestPodsFile = "openb_pod_list_test.csv"

const runTestPodsCSV = "name,cpu_milli,memory_mib,num_gpu,gpu_milli,gpu_spec,qos,pod_phase,creation_time,deletion_time,scheduled_time\n" +
	"openb-pod-0000,1000,1024,0,0,,LS,Running,0,1000,0\n" +
	"openb-pod-0001,1000,1024,0,0,,LS,Running,30,1030,30\n"

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
// counts the transitions written after it.
type fakeRecordStore struct {
	mu   sync.Mutex
	runs []*fakeRun
}

type fakeRun struct {
	header          map[string]any
	transitionLines int
}

func (s *fakeRecordStore) factory(evaluator.RecordRunInfo) (evaluator.RecordWriter, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := &fakeRun{}
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
	return nil
}

func (w *fakeRecordWriter) Close() error { return nil }

// run(--iterations=3) は、反復の回数だけ記録の実行ができ、どの実行の
// 1行目にも input.trace_name・input.trace_version・input.input_seed・
// input.rho・input.reference_commit と、動かしたスケジューラを指す
// scheduler.profile が正しく入り、どの実行も遷移を1件以上記録する。
// scheduler.profile は、集計（aggregate/aggregate.py の _diffs_for_group と
// build_rho_sections）がスケジューラを区別する鍵なので、書き違えると落ちずに
// 誤った集計になる。
func TestRun_WritesOneRunPerIteration(t *testing.T) {
	nodesPath := writeTempCSV(t, "nodes.csv", runTestNodesCSV)
	podsPath := writeTempCSV(t, runTestPodsFile, runTestPodsCSV)
	store := &fakeRecordStore{}

	var stderr bytes.Buffer
	code := run([]string{
		"--nodes=" + nodesPath,
		"--pods=" + podsPath,
		"--seed=1",
		"--ratio=1.0",
		"--rho=0.5",
		"--iterations=3",
	}, &stderr, store.factory, func() (string, error) { return "fake-reference-commit", nil })
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %s)", code, stderr.String())
	}

	if len(store.runs) != 3 {
		t.Fatalf("run count = %d, want 3", len(store.runs))
	}

	wantTraceVersion := strings.TrimSuffix(runTestPodsFile, ".csv")
	for _, r := range store.runs {
		input, _ := r.header["input"].(map[string]any)
		if input == nil {
			t.Fatalf("header is missing input: %v", r.header)
		}
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
		if got, _ := input["rho"].(float64); got != 0.5 {
			t.Errorf("input.rho = %v, want 0.5", got)
		}
		sched, _ := r.header["scheduler"].(map[string]any)
		if sched == nil {
			t.Fatalf("header is missing scheduler: %v", r.header)
		}
		// このコマンドは scheduler.Option を渡さないので、動くのは既定の
		// kube-scheduler のプロファイルだけである。
		if got, _ := sched["profile"].(string); got != "default kube-scheduler profile" {
			t.Errorf("scheduler.profile = %q, want %q", got, "default kube-scheduler profile")
		}
		if r.transitionLines == 0 {
			t.Errorf("run with header %v recorded no transitions", r.header)
		}
	}
}

// --iterations が1未満なら、何も実行せずに失敗する。
func TestRun_FailsWhenIterationsLessThanOne(t *testing.T) {
	nodesPath := writeTempCSV(t, "nodes.csv", runTestNodesCSV)
	podsPath := writeTempCSV(t, runTestPodsFile, runTestPodsCSV)
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

// reference のコミットを取れなければ、何も実行せずに失敗する。コミットを
// 名乗れないまま記録を書くと、どのツリーが動いたのか分からない記録ができる
// ためである。
func TestRun_FailsWhenReferenceCommitCannotBeObtained(t *testing.T) {
	nodesPath := writeTempCSV(t, "nodes.csv", runTestNodesCSV)
	podsPath := writeTempCSV(t, runTestPodsFile, runTestPodsCSV)
	store := &fakeRecordStore{}

	var stderr bytes.Buffer
	code := run([]string{
		"--nodes=" + nodesPath,
		"--pods=" + podsPath,
	}, &stderr, store.factory, func() (string, error) {
		return "", errors.New("git is unavailable")
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want nonzero")
	}
	if len(store.runs) != 0 {
		t.Errorf("run count = %d, want 0", len(store.runs))
	}
	if !strings.Contains(stderr.String(), "git is unavailable") {
		t.Errorf("stderr = %q, want it to carry the provider's error", stderr.String())
	}
}

// --rho が有限な数でなければ、何も実行せずに失敗する。+Inf は
// clusterdata.BuildInputVersion の検証（NaN と 0 以下だけを弾く）を素通りし、
// scenarioID が作る "rho+Inf" が definition.NewScenario の ID の制約
// （^[a-zA-Z][a-zA-Z0-9-]*$）に外れて panic するためである。
func TestRun_FailsWhenRhoIsNotFinite(t *testing.T) {
	for _, rho := range []string{"+Inf", "-Inf", "NaN"} {
		t.Run(rho, func(t *testing.T) {
			nodesPath := writeTempCSV(t, "nodes.csv", runTestNodesCSV)
			podsPath := writeTempCSV(t, runTestPodsFile, runTestPodsCSV)
			store := &fakeRecordStore{}

			var stderr bytes.Buffer
			code := run([]string{
				"--nodes=" + nodesPath,
				"--pods=" + podsPath,
				"--rho=" + rho,
			}, &stderr, store.factory, func() (string, error) { return "fake-reference-commit", nil })
			if code != 2 {
				t.Errorf("exit code = %d, want 2 (stderr: %s)", code, stderr.String())
			}
			if len(store.runs) != 0 {
				t.Errorf("run count = %d, want 0", len(store.runs))
			}
		})
	}
}

// pods CSV のファイル名が openb で始まらなければ、記録のヘッダが実際と
// 違うトレースを名乗ってしまうので、実行せずに失敗する。
func TestRun_FailsWhenPodsFileDoesNotStartWithTraceName(t *testing.T) {
	nodesPath := writeTempCSV(t, "nodes.csv", runTestNodesCSV)
	podsPath := writeTempCSV(t, "pods.csv", runTestPodsCSV)
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

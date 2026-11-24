//go:build integration

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const nodesCSV = "sn,cpu_milli,memory_mib,gpu,model\n" +
	"openb-node-0000,32000,262144,0,\n"

// creation_time 0, 30, 100 (投入期間100s), 実行時間1000sの3Podで、cpuのρは
// 1000*1000*3 / (32000*100) = 0.9375 になる。
const podsCSV = "name,cpu_milli,memory_mib,num_gpu,gpu_milli,gpu_spec,qos,pod_phase,creation_time,deletion_time,scheduled_time\n" +
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

// 本番と同じ形の引数で動かすと、使ったPodの件数・除いた件数・Failedの件数・
// 倍率をかける前と後のρが表示される。
func TestRun_PrintsSummary(t *testing.T) {
	nodesPath := writeTempCSV(t, "nodes.csv", nodesCSV)
	podsPath := writeTempCSV(t, "pods.csv", podsCSV)

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--nodes=" + nodesPath,
		"--pods=" + podsPath,
		"--seed=1",
		"--rho=1.1",
		"--ratio=1.0",
	}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %s)", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"used pods:     3",
		"excluded pods: 0",
		"failed pods:   0",
		"rho (after):   1.100000",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q, want it to contain %q", out, want)
		}
	}
}

// --out を渡すと、入力の版そのもの（イベントの列）がそのパスに JSONL で
// 書き出される。1行が1つのイベントで、種類・次のイベントまでの間隔・
// 対象のオブジェクトを持つ。要約だけでは、どの版で実験したかが残らない
// （units/U05.md「入力の版、記録、報告の書き出し先は、引数で受け取る」）。
func TestRun_WritesInputVersionToOutPath(t *testing.T) {
	nodesPath := writeTempCSV(t, "nodes.csv", nodesCSV)
	podsPath := writeTempCSV(t, "pods.csv", podsCSV)
	outPath := filepath.Join(t.TempDir(), "nested", "input-version.jsonl")

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--nodes=" + nodesPath,
		"--pods=" + podsPath,
		"--seed=1",
		"--rho=1.1",
		"--ratio=1.0",
		"--out=" + outPath,
	}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %s)", code, stderr.String())
	}
	content, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("failed to read %s: %v", outPath, err)
	}
	lines := strings.Split(strings.TrimRight(string(content), "\n"), "\n")
	// 1つのノードの Create、3つの Pod の Create、そのノードの Delete。
	if len(lines) != 5 {
		t.Fatalf("input version has %d line(s), want 5:\n%s", len(lines), content)
	}
	types := make([]string, 0, len(lines))
	for i, line := range lines {
		var event struct {
			EventType string          `json:"event_type"`
			Interval  string          `json:"interval"`
			Object    json.RawMessage `json:"object"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("line %d is not JSON: %v (%q)", i+1, err, line)
		}
		if event.Interval == "" {
			t.Errorf("line %d has no interval: %q", i+1, line)
		}
		if len(event.Object) == 0 {
			t.Errorf("line %d has no object: %q", i+1, line)
		}
		types = append(types, event.EventType)
	}
	if types[0] != "Create" || types[len(types)-1] != "Delete" {
		t.Errorf("event types = %v, want the first to be Create and the last Delete", types)
	}
	if !strings.Contains(string(content), "openb-pod-0000") {
		t.Errorf("input version does not name the sampled pod openb-pod-0000:\n%s", content)
	}
}

// --out のパスに書けないときは、終了コードが0でなく、stderr にその旨が出る。
// 入力の版が残らないまま実験を進めると、どの版で動かしたかが分からなくなる。
func TestRun_FailsWhenInputVersionCannotBeWritten(t *testing.T) {
	nodesPath := writeTempCSV(t, "nodes.csv", nodesCSV)
	podsPath := writeTempCSV(t, "pods.csv", podsCSV)
	// 既にファイルがあるパスの「下」には、ディレクトリを作れない。
	blocker := writeTempCSV(t, "blocker", "not a directory\n")

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--nodes=" + nodesPath,
		"--pods=" + podsPath,
		"--out=" + filepath.Join(blocker, "input-version.jsonl"),
	}, &stdout, &stderr)

	if code == 0 {
		t.Fatalf("exit code = 0, want nonzero")
	}
	if stderr.String() == "" {
		t.Errorf("stderr is empty, want an error message")
	}
}

// ノードのCSVのパスが存在しないときは、終了コードが0でなく、stderr に
// その旨が出て、stdout には要約が出ない。
func TestRun_FailsWhenNodesPathDoesNotExist(t *testing.T) {
	podsPath := writeTempCSV(t, "pods.csv", podsCSV)

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--nodes=/no/such/file.csv",
		"--pods=" + podsPath,
	}, &stdout, &stderr)

	if code == 0 {
		t.Fatalf("exit code = 0, want nonzero")
	}
	if stderr.String() == "" {
		t.Errorf("stderr is empty, want an error message")
	}
	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
}

// Pod のCSVが壊れている（列が足りない）と、BuildInputVersion がエラーを
// 返し、終了コードが0でなくなる。
func TestRun_FailsWhenPodsCSVIsMalformed(t *testing.T) {
	nodesPath := writeTempCSV(t, "nodes.csv", nodesCSV)
	podsPath := writeTempCSV(t, "pods.csv", "name,cpu_milli\nopenb-pod-0000,1000\n")

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--nodes=" + nodesPath,
		"--pods=" + podsPath,
	}, &stdout, &stderr)

	if code == 0 {
		t.Fatalf("exit code = 0, want nonzero")
	}
	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
}

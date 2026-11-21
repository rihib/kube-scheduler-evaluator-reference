//go:build integration

package main

import (
	"bytes"
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

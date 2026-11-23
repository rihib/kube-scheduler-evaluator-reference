//go:build integration

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pilotNodesCSV, pilotTestPodsFile and pilotTestPodsCSV are also defined,
// identically, in main_test.go: that file has no build tag, so it is
// compiled together with this one whenever -tags integration is passed, and
// redeclaring them here would be a build error. The same goes for
// writeTempCSV and profileLabel.

// recordHeader is the subset of the transition record's header line (units/U01.md)
// this test needs to tell runs apart.
type recordHeader struct {
	Input struct {
		Rho float64 `json:"rho"`
	} `json:"input"`
	Scheduler struct {
		Profile string `json:"profile"`
	} `json:"scheduler"`
}

func readHeader(t *testing.T, path string) recordHeader {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read record file %s: %v", path, err)
	}
	firstLine := strings.SplitN(string(data), "\n", 2)[0]
	var h recordHeader
	if err := json.Unmarshal([]byte(firstLine), &h); err != nil {
		t.Fatalf("header line of %s is not valid JSON: %v (line: %s)", path, err, firstLine)
	}
	return h
}

// 予備実験の4回の実行（spec.md シナリオ3: ρ 0.5/1.1 x LeastAllocated/MostAllocated）を
// まとめて動かすと、組ごとに記録のファイルが1つずつでき、1行目の rho と
// scheduler.profile で、どの組の実行かが区別できる。
func TestRun_WritesOneRecordFilePerCombo(t *testing.T) {
	nodesPath := writeTempCSV(t, "nodes.csv", pilotTestNodesCSV)
	podsPath := writeTempCSV(t, pilotTestPodsFile, pilotTestPodsCSV)
	outDir := t.TempDir()

	var stderr bytes.Buffer
	code := run([]string{
		"--nodes=" + nodesPath,
		"--pods=" + podsPath,
		"--seed=1",
		"--ratio=1.0",
		"--rhos=0.5,1.1",
		"--iterations=1",
		"--out=" + outDir,
	}, &stderr, nil, func() (string, error) { return "fake-reference-commit", nil })
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %s)", code, stderr.String())
	}

	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatalf("failed to read output dir: %v", err)
	}
	if len(entries) != 4 {
		t.Fatalf("record file count = %d, want 4: %v", len(entries), entries)
	}

	wantCombos := map[string]bool{
		"0.5-LeastAllocated": false, "0.5-MostAllocated": false,
		"1.1-LeastAllocated": false, "1.1-MostAllocated": false,
	}
	for _, e := range entries {
		h := readHeader(t, filepath.Join(outDir, e.Name()))
		key := fmt.Sprintf("%v-%s", h.Input.Rho, profileLabel(t, h.Scheduler.Profile))
		if _, ok := wantCombos[key]; !ok {
			t.Errorf("record file %s has unexpected combo %s", e.Name(), key)
			continue
		}
		wantCombos[key] = true
	}
	for combo, seen := range wantCombos {
		if !seen {
			t.Errorf("no record file found for combo %s", combo)
		}
	}
}

// Pod の CSV のパスが存在しないときは、終了コードが0でなく、記録のファイルは
// 1つもできない。
func TestRun_FailsWhenPodsPathDoesNotExist(t *testing.T) {
	nodesPath := writeTempCSV(t, "nodes.csv", pilotTestNodesCSV)
	outDir := t.TempDir()

	var stderr bytes.Buffer
	code := run([]string{
		"--nodes=" + nodesPath,
		"--pods=/no/such/file.csv",
		"--out=" + outDir,
	}, &stderr, nil, func() (string, error) { return "fake-reference-commit", nil })
	if code == 0 {
		t.Fatalf("exit code = 0, want nonzero")
	}
	entries, err := os.ReadDir(outDir)
	if err == nil && len(entries) != 0 {
		t.Errorf("record files = %v, want none", entries)
	}
}

//go:build integration

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// pilotTestNodesCSV, pilotTestPodsFile and pilotTestPodsCSV are also defined,
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

// initTempGitRepo creates a git repository with one commit in a temporary
// directory, makes it the test's working directory, and returns that commit.
// referenceCommit() asks git about the working directory it runs in, so the
// contract tests below need a repository of their own: this repository's own
// tree gets dirty while work is in progress, which would decide the answer
// for them.
func initTempGitRepo(t *testing.T) (dir, headCommit string) {
	t.Helper()
	dir = t.TempDir()
	t.Chdir(dir)

	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("first\n"), 0o644); err != nil {
		t.Fatalf("failed to write tracked.txt: %v", err)
	}
	// user.name/user.email and commit.gpgsign are passed per command so the
	// commit does not depend on (or get blocked by) the developer's own git
	// configuration.
	for _, args := range [][]string{
		{"-c", "init.defaultBranch=main", "init", "--quiet"},
		{"add", "tracked.txt"},
		{
			"-c", "user.name=runpilot test", "-c", "user.email=test@example.com",
			"-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "first",
		},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v (output: %s)", args, err, out)
		}
	}

	revParse := exec.Command("git", "rev-parse", "HEAD")
	revParse.Dir = dir
	out, err := revParse.Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD failed: %v", err)
	}
	return dir, strings.TrimSpace(string(out))
}

// 本物の git に問い合わせる経路（ビルド情報に vcs.revision が無い、
// "go run ./cmd/runpilot" という普段の動かし方で通る経路）で、作業ツリーが
// コミットと同じなら、記録の1行目のコミットは HEAD をそのまま名乗る。
func TestReferenceCommit_UsesGitHeadWhenTreeIsClean(t *testing.T) {
	_, head := initTempGitRepo(t)

	commit, err := referenceCommit()
	if err != nil {
		t.Fatalf("referenceCommit() failed: %v", err)
	}
	if commit != head {
		t.Errorf("commit = %q, want %q (git rev-parse HEAD of the clean repository)", commit, head)
	}
}

// 同じ経路で、作業ツリーが汚れていれば "-dirty" が付く。記録が、実際に動いた
// ツリーと違うコミットを名乗らないため。
func TestReferenceCommit_MarksDirtyWhenGitTreeIsDirty(t *testing.T) {
	dir, head := initTempGitRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatalf("failed to dirty tracked.txt: %v", err)
	}

	commit, err := referenceCommit()
	if err != nil {
		t.Fatalf("referenceCommit() failed: %v", err)
	}
	if commit != head+"-dirty" {
		t.Errorf("commit = %q, want %q", commit, head+"-dirty")
	}
}

// 同じ経路で、HEAD そのものを git に聞けなかったら失敗する。コミットを名乗れない
// まま記録を書くと、どのツリーが動いたのか分からない記録ができるためである。
func TestReferenceCommit_FailsWhenGitCannotTellTheHeadCommit(t *testing.T) {
	// git init していない一時フォルダでは git rev-parse HEAD が
	// "not a git repository" で失敗する。initTempGitRepo は呼ばない。
	t.Chdir(t.TempDir())

	commit, err := referenceCommit()
	if err == nil {
		t.Fatalf("referenceCommit() = %q, want an error", commit)
	}
	if !strings.Contains(err.Error(), "git rev-parse HEAD failed") {
		t.Errorf("error = %q, want it to say that git rev-parse HEAD failed", err)
	}
}

// 同じ経路で、HEAD は取れても作業ツリーが汚れているかを git に聞けなかったら、
// クリーンな木と決めつけずに失敗する。"-dirty" の付かないコミットは、実際に
// 動いたツリーがコミットと同じだという主張だからである。
func TestReferenceCommit_FailsWhenGitCannotTellWhetherTreeIsDirty(t *testing.T) {
	dir, _ := initTempGitRepo(t)
	// .git/index の中身を壊すと、git status --porcelain だけが失敗し
	// （index file smaller than expected）、git rev-parse HEAD は成功する。
	// 権限ではなく中身を壊すので、root で流しても同じになる。
	if err := os.WriteFile(filepath.Join(dir, ".git", "index"), []byte("garbage-not-an-index"), 0o644); err != nil {
		t.Fatalf("failed to corrupt .git/index: %v", err)
	}

	commit, err := referenceCommit()
	if err == nil {
		t.Fatalf("referenceCommit() = %q, want an error", commit)
	}
	if !strings.Contains(err.Error(), "git rev-parse HEAD succeeded") {
		t.Errorf("error = %q, want it to say that git rev-parse HEAD succeeded", err)
	}
}

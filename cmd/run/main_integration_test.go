//go:build integration

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runTestNodesCSV, runTestPodsFile, runTestPodsCSV and writeTempCSV are also
// defined, identically, in main_test.go: that file has no build tag, so it
// is compiled together with this one whenever -tags integration is passed,
// and redeclaring them here would be a build error.

// recordHeader is the subset of the transition record's header line
// (units/U01.md) this test needs.
type recordHeader struct {
	Input struct {
		Rho float64 `json:"rho"`
	} `json:"input"`
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

// 本物の記録の出力先（一時フォルダ）に向けて動かすと、記録のファイルが1つ
// できて、1行目の input.rho が指定した値になっている。
func TestRun_WritesOneRecordFileToRealOutputDir(t *testing.T) {
	nodesPath := writeTempCSV(t, "nodes.csv", runTestNodesCSV)
	podsPath := writeTempCSV(t, runTestPodsFile, runTestPodsCSV)
	outDir := t.TempDir()

	var stderr bytes.Buffer
	code := run([]string{
		"--nodes=" + nodesPath,
		"--pods=" + podsPath,
		"--seed=1",
		"--shrink=1.0",
		"--rho=0.03",
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
	if len(entries) != 1 {
		t.Fatalf("record file count = %d, want 1: %v", len(entries), entries)
	}
	h := readHeader(t, filepath.Join(outDir, entries[0].Name()))
	if h.Input.Rho != 0.03 {
		t.Errorf("input.rho = %v, want 0.03", h.Input.Rho)
	}
}

// このコマンドが書いた記録を、集計のコマンド（aggregate/aggregate.py）が
// そのまま読んで報告にできる。記録を書く側と読む側は別の言語なので、ヘッダの
// 項目の名前（aggregate.py の parse_record が必須で読む input.*・execution.*・
// scheduler.profile）が食い違っても、どちらかのテストだけでは落ちない。報告に
// 動かした ρ の節ができることまで確かめ、ヘッダの値が読み側に届いていることを
// 見る。
func TestRun_WritesRecordAggregateCanTurnIntoReport(t *testing.T) {
	nodesPath := writeTempCSV(t, "nodes.csv", runTestNodesCSV)
	podsPath := writeTempCSV(t, runTestPodsFile, runTestPodsCSV)
	recordsDir := t.TempDir()
	reportDir := t.TempDir()

	var stderr bytes.Buffer
	code := run([]string{
		"--nodes=" + nodesPath,
		"--pods=" + podsPath,
		"--seed=1",
		"--shrink=1.0",
		"--rho=0.03",
		"--iterations=1",
		"--out=" + recordsDir,
	}, &stderr, nil, func() (string, error) { return "fake-reference-commit", nil })
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %s)", code, stderr.String())
	}

	// aggregate.py は PEP723 のインラインスクリプトなので、uv が依存を解決
	// して流す。uv が無ければこの境目の検査が静かに消えるので、飛ばさずに
	// 落とす。
	aggregate, err := filepath.Abs(filepath.Join("..", "..", "aggregate", "aggregate.py"))
	if err != nil {
		t.Fatalf("failed to resolve aggregate.py's path: %v", err)
	}
	cmd := exec.Command("uv", "run", aggregate,
		"--records-dir="+recordsDir, "--output-dir="+reportDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("aggregate.py could not read the record this command wrote: %v (output: %s)", err, out)
	}

	report, err := os.ReadFile(filepath.Join(reportDir, "report.md"))
	if err != nil {
		t.Fatalf("failed to read the report aggregate.py wrote: %v", err)
	}
	if !strings.Contains(string(report), "ρ = 0.03") {
		t.Errorf("report has no section for the ρ this command ran:\n%s", report)
	}
}

// Pod の CSV のパスが存在しないときは、終了コードが0でなく、記録のファイルは
// 1つもできない。
func TestRun_FailsWhenPodsPathDoesNotExist(t *testing.T) {
	nodesPath := writeTempCSV(t, "nodes.csv", runTestNodesCSV)
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

// ノードの CSV のパスが存在しないときも、終了コードが0でなく、記録のファイルは
// 1つもできない。
func TestRun_FailsWhenNodesPathDoesNotExist(t *testing.T) {
	podsPath := writeTempCSV(t, runTestPodsFile, runTestPodsCSV)
	outDir := t.TempDir()

	var stderr bytes.Buffer
	code := run([]string{
		"--nodes=/no/such/file.csv",
		"--pods=" + podsPath,
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
	// user.name/user.email と commit.gpgsign はコマンドごとに渡し、
	// 開発者自身の git の設定に依存したり、それでブロックされたりしないよう
	// にする。
	for _, args := range [][]string{
		{"-c", "init.defaultBranch=main", "init", "--quiet"},
		{"add", "tracked.txt"},
		{
			"-c", "user.name=run test", "-c", "user.email=test@example.com",
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
// "go run ./cmd/run" という普段の動かし方で通る経路）で、作業ツリーが
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

// 同じ経路で、HEAD そのものを git に聞けなかったら失敗する。コミットを
// 名乗れないまま記録を書くと、どのツリーが動いたのか分からない記録ができる
// ためである。
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

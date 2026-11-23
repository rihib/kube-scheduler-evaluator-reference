// Command runpilot runs experiment1's pilot (spec.md scenario 3): one openb
// input version, against every combination of target rho and scheduler
// profile, each run the requested number of times. Each run writes its own
// transition record (units/U03.md), whose header line identifies the
// combination it belongs to.
package main

import (
	"io"
	"os"

	"github.com/pfnet/kube-scheduler-evaluator/cmd/evaluator"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, nil, nil))
}

// run parses args in the same shape as the real command line, builds one
// input version per target rho from the openb CSVs, and runs it against both
// scheduler profiles the requested number of iterations. recordWriterFactory
// and referenceCommitProvider are injected so tests can pass Fakes for where
// the record is written and for reference's own commit (units/U03.md
// Implementation Decisions: "コマンドのアダプターは、記録の書き出し先と、
// reference のコミットを取るものを外から受け取る"); nil selects their real
// implementations.
//
// TODO(U03): not yet implemented; always reports success without running anything.
func run(
	args []string, stdout, stderr io.Writer,
	recordWriterFactory evaluator.RecordWriterFactory,
	referenceCommitProvider func() (string, error),
) int {
	return 0
}

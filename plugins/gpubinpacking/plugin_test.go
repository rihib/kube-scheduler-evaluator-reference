package gpubinpacking

import "testing"

func TestDemoScores(t *testing.T) {
	if small, large := currentUtilizationScore(0, 1), currentUtilizationScore(4, 8); small >= large {
		t.Fatalf("current utilization scores: small=%d large=%d, want large preferred", small, large)
	}

	small, err := bestFitScore(0, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	large, err := bestFitScore(4, 8, 1)
	if err != nil {
		t.Fatal(err)
	}
	if small <= large {
		t.Fatalf("best-fit scores: small=%d large=%d, want small preferred", small, large)
	}
}

func TestBestFitRejectsInfeasibleNode(t *testing.T) {
	if _, err := bestFitScore(4, 8, 5); err == nil {
		t.Fatal("expected an error")
	}
}

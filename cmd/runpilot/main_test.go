package main

import (
	"strings"
	"testing"
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

package main

import "testing"

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

package core

import "testing"

func TestUSDBudgetParsing(t *testing.T) {
	for _, s := range []string{"-1", "1.-1", "+1", "NaN", "1e6", "0.0000001", "1000001", ".5", "1.", "1.2.3"} {
		if _, e := ParseUSD(s); e == nil {
			t.Errorf("accepted invalid budget %q", s)
		}
	}
	for s, want := range map[string]int64{"0": 0, "0.000001": 1, "1.25": 1250000, "12.999999": 12999999} {
		got, e := ParseUSD(s)
		if e != nil || got != want {
			t.Errorf("%s: got %d %v", s, got, e)
		}
		if back, _ := ParseUSD(USD(got)); back != got {
			t.Fatal("decimal roundtrip lost precision")
		}
	}
}

package config

import "testing"

func TestRunTimeBudget(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		want        int
	}{
		{"default twenty minutes", "", 1200},
		{"invalid value uses default", "invalid", 1200},
		{"nonpositive value uses default", "0", 1200},
		{"explicit override", "1800", 1800},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ATOMS_RUN_SECONDS", tc.value)
			if got := Load().RunSeconds; got != tc.want {
				t.Fatalf("run budget = %d seconds, want %d", got, tc.want)
			}
		})
	}
}

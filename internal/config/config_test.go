package config

import "testing"

func TestRunTimeBudget(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		want        int
	}{
		{"default one hour", "", 3600},
		{"invalid value uses default", "invalid", 3600},
		{"nonpositive value uses default", "0", 3600},
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

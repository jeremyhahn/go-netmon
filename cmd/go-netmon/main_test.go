package main

import "testing"

// TestDashboardPath pins both halves of the contract the native deployment
// depends on: the container default is unchanged, so the image keeps working,
// and DASHBOARD_PATH relocates the directory, so a host install can write
// somewhere that exists.
func TestDashboardPath(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want string
	}{
		{"unset falls back to the container mount point", "", "/app/dashboards"},
		{"set relocates the directory", "/var/lib/go-netmon/dashboards", "/var/lib/go-netmon/dashboards"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("DASHBOARD_PATH", tt.env)
			if got := dashboardPath(); got != tt.want {
				t.Errorf("dashboardPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

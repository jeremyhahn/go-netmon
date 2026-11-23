package importer

import (
	"strings"
	"testing"
)

func TestParseSupportListReader(t *testing.T) {
	tests := []struct {
		name        string
		html        string
		wantMIBs    []string
		wantErr     bool
		errContains string
	}{
		{
			name: "cisco support list with table",
			html: `
				<html>
					<body>
						<table>
							<tr><td>CISCO-SYSTEM-MIB</td></tr>
							<tr><td>CISCO-MEMORY-POOL-MIB</td></tr>
							<tr><td>IF-MIB</td></tr>
						</table>
					</body>
				</html>
			`,
			wantMIBs: []string{"CISCO-SYSTEM-MIB", "CISCO-MEMORY-POOL-MIB", "IF-MIB"},
			wantErr:  false,
		},
		{
			name: "support list with list items",
			html: `
				<html>
					<body>
						<ul>
							<li>CISCO-PROCESS-MIB</li>
							<li>CISCO-ENVMON-MIB</li>
						</ul>
					</body>
				</html>
			`,
			wantMIBs: []string{"CISCO-PROCESS-MIB", "CISCO-ENVMON-MIB"},
			wantErr:  false,
		},
		{
			name: "mixed case and suffixes",
			html: `
				<html>
					<body>
						<table>
							<tr><td>cisco-system-mib</td></tr>
							<tr><td>CISCO-MEMORY-POOL-MIB.my</td></tr>
							<tr><td>IF-MIB (v2)</td></tr>
						</table>
					</body>
				</html>
			`,
			wantMIBs: []string{"CISCO-SYSTEM-MIB", "CISCO-MEMORY-POOL-MIB", "IF-MIB"},
			wantErr:  false,
		},
		{
			name: "duplicate MIBs",
			html: `
				<html>
					<body>
						<table>
							<tr><td>CISCO-SYSTEM-MIB</td></tr>
							<tr><td>CISCO-SYSTEM-MIB</td></tr>
							<tr><td>IF-MIB</td></tr>
						</table>
					</body>
				</html>
			`,
			wantMIBs: []string{"CISCO-SYSTEM-MIB", "IF-MIB"},
			wantErr:  false,
		},
		{
			name: "RFC MIBs",
			html: `
				<html>
					<body>
						<table>
							<tr><td>RFC1213-MIB</td></tr>
							<tr><td>SNMPv2-MIB</td></tr>
						</table>
					</body>
				</html>
			`,
			wantMIBs: []string{"RFC1213-MIB", "SNMPV2-MIB"},
			wantErr:  false,
		},
		{
			name: "no MIBs found",
			html: `
				<html>
					<body>
						<p>This is just regular text without any MIB names.</p>
					</body>
				</html>
			`,
			wantErr:     true,
			errContains: "no MIBs found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := strings.NewReader(tt.html)
			mibs, err := ParseSupportListReader(r)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got none")
				} else if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("error = %v, want error containing %q", err, tt.errContains)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// Check that all wanted MIBs are present
			mibMap := make(map[string]bool)
			for _, mib := range mibs {
				mibMap[mib] = true
			}

			for _, want := range tt.wantMIBs {
				if !mibMap[want] {
					t.Errorf("missing MIB %q in result %v", want, mibs)
				}
			}

			// Check no extra MIBs
			if len(mibs) != len(tt.wantMIBs) {
				t.Errorf("got %d MIBs, want %d: %v", len(mibs), len(tt.wantMIBs), mibs)
			}
		})
	}
}

func TestIsMIBName(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		// Valid MIB names
		{"CISCO-SYSTEM-MIB", true},
		{"CISCO-MEMORY-POOL-MIB", true},
		{"IF-MIB", true},
		{"SNMPv2-MIB", true},
		{"RFC1213-MIB", true},
		{"TCP-MIB", true},
		{"UDP-MIB", true},
		{"IP-MIB", true},
		{"IANAifType-MIB", true},
		{"CISCO-CAPABILITY", true},

		// Invalid
		{"", false},
		{"AB", false},                               // too short
		{strings.Repeat("A", 101), false},           // too long
		{"Some random text", false},                 // no MIB pattern
		{"This is a description of the MIB", false}, // contains -MIB but not a MIB name
		{"Version 1.2.3", false},                    // version string
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := isMIBName(tt.input)
			if got != tt.want {
				t.Errorf("isMIBName(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestNormalizeMIBName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"CISCO-SYSTEM-MIB", "CISCO-SYSTEM-MIB"},
		{"cisco-system-mib", "CISCO-SYSTEM-MIB"},
		{"CISCO-SYSTEM-MIB.my", "CISCO-SYSTEM-MIB"},
		{"CISCO-SYSTEM-MIB.mib", "CISCO-SYSTEM-MIB"},
		{"IF-MIB (v2)", "IF-MIB"},
		{"IF-MIB v2", "IF-MIB"},
		{"  CISCO-SYSTEM-MIB  ", "CISCO-SYSTEM-MIB"},
		{"", ""},
		{"A", ""},                      // too short after normalization
		{"AB", ""},                     // too short after normalization
		{"with spaces inside", "WITH"}, // takes first word (before space)
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeMIBName(tt.input)
			if got != tt.want {
				t.Errorf("normalizeMIBName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseSupportList_FileErrors(t *testing.T) {
	tests := []struct {
		name        string
		file        string
		wantErr     bool
		errContains string
	}{
		{
			name:        "nonexistent file",
			file:        "/nonexistent/file.html",
			wantErr:     true,
			errContains: "failed to open HTML file",
		},
		{
			name:        "empty filename",
			file:        "",
			wantErr:     true,
			errContains: "failed to open HTML file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseSupportList(tt.file)
			if !tt.wantErr {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				return
			}

			if err == nil {
				t.Error("expected error but got none")
				return
			}

			if !strings.Contains(err.Error(), tt.errContains) {
				t.Errorf("error = %v, want error containing %q", err, tt.errContains)
			}
		})
	}
}

func TestParseMultipleSupportLists(t *testing.T) {
	tests := []struct {
		name    string
		files   []string
		wantErr bool
	}{
		{
			name:    "no files",
			files:   []string{},
			wantErr: true,
		},
		{
			name:    "single nonexistent file",
			files:   []string{"/nonexistent/file.html"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseMultipleSupportLists(tt.files)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseMultipleSupportLists() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

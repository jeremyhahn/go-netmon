package builder

import (
	"strings"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/grafana"
	"github.com/jeremyhahn/go-netmon/internal/mib"
)

func TestNewPanelGenerator(t *testing.T) {
	pg := NewPanelGenerator("snmp__")

	if pg.queryBuilder == nil {
		t.Error("NewPanelGenerator() queryBuilder is nil")
	}

	if pg.nextPanelID != 1 {
		t.Errorf("NewPanelGenerator() nextPanelID = %v, want 1", pg.nextPanelID)
	}
}

func TestPanelGenerator_DeterminePanelType(t *testing.T) {
	pg := NewPanelGenerator("snmp__")

	tests := []struct {
		name string
		oid  *mib.OIDMetadata
		want PanelType
	}{
		{
			name: "counter becomes timeseries",
			oid: &mib.OIDMetadata{
				Type: "Counter32",
				Name: "ifInOctets",
			},
			want: PanelTypeTimeSeries,
		},
		{
			name: "counter64 becomes timeseries",
			oid: &mib.OIDMetadata{
				Type: "Counter64",
				Name: "ifHCInOctets",
			},
			want: PanelTypeTimeSeries,
		},
		{
			name: "percentage becomes gauge",
			oid: &mib.OIDMetadata{
				Type: "Integer32",
				Name: "cpuUtilization",
			},
			want: PanelTypeGauge,
		},
		{
			name: "gauge becomes stat",
			oid: &mib.OIDMetadata{
				Type: "Gauge32",
				Name: "memoryUsed",
			},
			want: PanelTypeStat,
		},
		{
			name: "string becomes table",
			oid: &mib.OIDMetadata{
				Type: "OctetString",
				Name: "sysDescr",
			},
			want: PanelTypeTable,
		},
		{
			name: "integer becomes stat",
			oid: &mib.OIDMetadata{
				Type: "Integer32",
				Name: "someValue",
			},
			want: PanelTypeStat,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pg.DeterminePanelType(tt.oid)
			if got != tt.want {
				t.Errorf("DeterminePanelType() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPanelGenerator_GenerateScalarPanel(t *testing.T) {
	pg := NewPanelGenerator("snmp__")

	tests := []struct {
		name      string
		oid       *mib.OIDMetadata
		gridPos   grafana.GridPos
		wantErr   bool
		wantType  string
		checkFunc func(*testing.T, *grafana.Panel)
	}{
		{
			name: "counter scalar",
			oid: &mib.OIDMetadata{
				OID:      "1.3.6.1.2.1.2.1.0",
				Name:     "ifNumber",
				Type:     "Counter32",
				IsScalar: true,
			},
			gridPos:  grafana.GridPos{H: 8, W: 12, X: 0, Y: 0},
			wantErr:  false,
			wantType: string(PanelTypeTimeSeries),
			checkFunc: func(t *testing.T, p *grafana.Panel) {
				if len(p.Targets) == 0 {
					t.Error("GenerateScalarPanel() has no targets")
				}
				if !strings.Contains(p.Targets[0].Expr, "rate(") {
					t.Error("Counter panel should have rate() in query")
				}
			},
		},
		{
			// mib.OIDMetadata.IsCounter() treats TimeTicks as counter-like
			// (monotonically increasing), so sysUpTime is not a suitable
			// fixture for a gauge scalar; use an actual gauge type instead.
			name: "gauge scalar",
			oid: &mib.OIDMetadata{
				OID:      "1.3.6.1.2.1.25.2.3.1.5",
				Name:     "hrStorageUsed",
				Type:     "Integer32",
				IsScalar: true,
			},
			gridPos:  grafana.GridPos{H: 8, W: 12, X: 0, Y: 0},
			wantErr:  false,
			wantType: string(PanelTypeStat),
		},
		{
			name: "non-scalar OID",
			oid: &mib.OIDMetadata{
				OID:      "1.3.6.1.2.1.2.2",
				Name:     "ifTable",
				Type:     "Sequence",
				IsScalar: false,
				IsTable:  true,
			},
			gridPos: grafana.GridPos{H: 8, W: 12, X: 0, Y: 0},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pg.ResetPanelID()
			panel, err := pg.GenerateScalarPanel(tt.oid, tt.gridPos)

			if (err != nil) != tt.wantErr {
				t.Errorf("GenerateScalarPanel() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if err != nil {
				return
			}

			if panel.Type != tt.wantType {
				t.Errorf("GenerateScalarPanel() Type = %v, want %v", panel.Type, tt.wantType)
			}

			if panel.ID != 1 {
				t.Errorf("GenerateScalarPanel() ID = %v, want 1", panel.ID)
			}

			if panel.GridPos != tt.gridPos {
				t.Errorf("GenerateScalarPanel() GridPos = %v, want %v", panel.GridPos, tt.gridPos)
			}

			if tt.checkFunc != nil {
				tt.checkFunc(t, panel)
			}
		})
	}
}

func TestPanelGenerator_GenerateCounterPanel(t *testing.T) {
	pg := NewPanelGenerator("snmp__")

	tests := []struct {
		name    string
		oid     *mib.OIDMetadata
		gridPos grafana.GridPos
		wantErr bool
	}{
		{
			name: "valid counter",
			oid: &mib.OIDMetadata{
				OID:  "1.3.6.1.2.1.2.2.1.10",
				Name: "ifInOctets",
				Type: "Counter32",
			},
			gridPos: grafana.GridPos{H: 8, W: 12, X: 0, Y: 0},
			wantErr: false,
		},
		{
			// mib.OIDMetadata.IsCounter() treats TimeTicks as counter-like
			// (monotonically increasing), so sysUpTime is not a suitable
			// fixture for a non-counter OID; use an actual gauge type instead.
			name: "non-counter OID",
			oid: &mib.OIDMetadata{
				OID:  "1.3.6.1.2.1.25.2.3.1.5",
				Name: "hrStorageUsed",
				Type: "Integer32",
			},
			gridPos: grafana.GridPos{H: 8, W: 12, X: 0, Y: 0},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pg.ResetPanelID()
			panel, err := pg.GenerateCounterPanel(tt.oid, tt.gridPos)

			if (err != nil) != tt.wantErr {
				t.Errorf("GenerateCounterPanel() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if err != nil {
				return
			}

			if panel.Type != string(PanelTypeTimeSeries) {
				t.Errorf("GenerateCounterPanel() Type = %v, want %v", panel.Type, PanelTypeTimeSeries)
			}

			if len(panel.Targets) == 0 {
				t.Error("GenerateCounterPanel() has no targets")
				return
			}

			if !strings.Contains(panel.Targets[0].Expr, "rate(") {
				t.Error("Counter panel should have rate() in query")
			}
		})
	}
}

func TestPanelGenerator_GenerateGaugePanel(t *testing.T) {
	pg := NewPanelGenerator("snmp__")

	tests := []struct {
		name     string
		oid      *mib.OIDMetadata
		gridPos  grafana.GridPos
		wantErr  bool
		wantType string
	}{
		{
			name: "percentage gauge",
			oid: &mib.OIDMetadata{
				OID:  "1.3.6.1.2.1.25.3.3.1.2",
				Name: "hrProcessorLoadPercent",
				Type: "Integer32",
			},
			gridPos:  grafana.GridPos{H: 6, W: 12, X: 0, Y: 0},
			wantErr:  false,
			wantType: string(PanelTypeGauge),
		},
		{
			name: "regular gauge",
			oid: &mib.OIDMetadata{
				OID:  "1.3.6.1.2.1.25.2.3.1.5",
				Name: "hrStorageUsed",
				Type: "Integer32",
			},
			gridPos:  grafana.GridPos{H: 6, W: 12, X: 0, Y: 0},
			wantErr:  false,
			wantType: string(PanelTypeStat),
		},
		{
			name: "non-gauge OID",
			oid: &mib.OIDMetadata{
				OID:  "1.3.6.1.2.1.2.2.1.10",
				Name: "ifInOctets",
				Type: "Counter32",
			},
			gridPos: grafana.GridPos{H: 6, W: 12, X: 0, Y: 0},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pg.ResetPanelID()
			panel, err := pg.GenerateGaugePanel(tt.oid, tt.gridPos)

			if (err != nil) != tt.wantErr {
				t.Errorf("GenerateGaugePanel() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if err != nil {
				return
			}

			if panel.Type != tt.wantType {
				t.Errorf("GenerateGaugePanel() Type = %v, want %v", panel.Type, tt.wantType)
			}
		})
	}
}

func TestPanelGenerator_GenerateTablePanels(t *testing.T) {
	pg := NewPanelGenerator("snmp__")

	table := &mib.TableInfo{
		OID:         "1.3.6.1.2.1.2.2",
		Name:        "ifTable",
		Description: "Network Interface Table",
		Indexes:     []string{"ifIndex"},
		Columns:     []string{"1.3.6.1.2.1.2.2.1.2", "1.3.6.1.2.1.2.2.1.10"},
	}

	columns := []*mib.OIDMetadata{
		{
			OID:      "1.3.6.1.2.1.2.2.1.2",
			Name:     "ifDescr",
			Type:     "DisplayString",
			IsColumn: true,
		},
		{
			OID:      "1.3.6.1.2.1.2.2.1.10",
			Name:     "ifInOctets",
			Type:     "Counter32",
			IsColumn: true,
		},
		{
			OID:      "1.3.6.1.2.1.2.2.1.5",
			Name:     "ifSpeed",
			Type:     "Gauge32",
			IsColumn: true,
		},
	}

	tests := []struct {
		name      string
		table     *mib.TableInfo
		columns   []*mib.OIDMetadata
		startY    int
		wantErr   bool
		wantCount int
	}{
		{
			name:      "valid table with mixed columns",
			table:     table,
			columns:   columns,
			startY:    0,
			wantErr:   false,
			wantCount: 3, // 1 counter, 1 gauge, 1 table for strings
		},
		{
			name:    "empty columns",
			table:   table,
			columns: []*mib.OIDMetadata{},
			startY:  0,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pg.ResetPanelID()
			panels, err := pg.GenerateTablePanels(tt.table, tt.columns, tt.startY)

			if (err != nil) != tt.wantErr {
				t.Errorf("GenerateTablePanels() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if err != nil {
				return
			}

			if len(panels) != tt.wantCount {
				t.Errorf("GenerateTablePanels() panel count = %v, want %v", len(panels), tt.wantCount)
			}

			// Check that panel IDs are sequential
			for i, panel := range panels {
				if panel.ID != i+1 {
					t.Errorf("Panel %d has ID %v, want %v", i, panel.ID, i+1)
				}
			}
		})
	}
}

func TestPanelGenerator_CalculateGridPos(t *testing.T) {
	pg := NewPanelGenerator("snmp__")

	tests := []struct {
		name       string
		panelIndex int
		rowIndex   int
		want       grafana.GridPos
	}{
		{
			name:       "first panel",
			panelIndex: 0,
			rowIndex:   0,
			want:       grafana.GridPos{H: 8, W: 12, X: 0, Y: 0},
		},
		{
			name:       "second panel (same row)",
			panelIndex: 1,
			rowIndex:   0,
			want:       grafana.GridPos{H: 8, W: 12, X: 12, Y: 0},
		},
		{
			name:       "third panel (new row)",
			panelIndex: 2,
			rowIndex:   0,
			want:       grafana.GridPos{H: 8, W: 12, X: 0, Y: 8},
		},
		{
			name:       "fourth panel",
			panelIndex: 3,
			rowIndex:   0,
			want:       grafana.GridPos{H: 8, W: 12, X: 12, Y: 8},
		},
		{
			name:       "with row offset",
			panelIndex: 0,
			rowIndex:   2,
			want:       grafana.GridPos{H: 8, W: 12, X: 0, Y: 16},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pg.CalculateGridPos(tt.panelIndex, tt.rowIndex)
			if got != tt.want {
				t.Errorf("CalculateGridPos() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPanelGenerator_determineUnit(t *testing.T) {
	pg := NewPanelGenerator("snmp__")

	tests := []struct {
		name string
		oid  *mib.OIDMetadata
		want string
	}{
		{
			name: "explicit unit",
			oid: &mib.OIDMetadata{
				Name:  "someValue",
				Units: "seconds",
			},
			want: "s",
		},
		{
			name: "percent from name",
			oid: &mib.OIDMetadata{
				Name: "cpuUtilizationPercent",
			},
			want: "percent",
		},
		{
			name: "bytes counter (becomes bps)",
			oid: &mib.OIDMetadata{
				Name: "ifInOctets",
				Type: "Counter32",
			},
			want: "bps",
		},
		{
			name: "packets counter (becomes pps)",
			oid: &mib.OIDMetadata{
				Name: "ifInPkts",
				Type: "Counter32",
			},
			want: "pps",
		},
		{
			name: "temperature",
			oid: &mib.OIDMetadata{
				Name: "deviceTemperature",
			},
			want: "celsius",
		},
		{
			name: "no specific unit",
			oid: &mib.OIDMetadata{
				Name: "someValue",
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pg.determineUnit(tt.oid)
			if got != tt.want {
				t.Errorf("determineUnit() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPanelGenerator_isPercentage(t *testing.T) {
	pg := NewPanelGenerator("snmp__")

	tests := []struct {
		name string
		oid  *mib.OIDMetadata
		want bool
	}{
		{
			name: "explicit percent in units",
			oid: &mib.OIDMetadata{
				Name:  "someValue",
				Units: "percent",
			},
			want: true,
		},
		{
			name: "percent in name",
			oid: &mib.OIDMetadata{
				Name: "cpuPercent",
			},
			want: true,
		},
		{
			name: "utilization in name",
			oid: &mib.OIDMetadata{
				Name: "cpuUtilization",
			},
			want: true,
		},
		{
			name: "usage in name",
			oid: &mib.OIDMetadata{
				Name: "memoryUsage",
			},
			want: true,
		},
		{
			name: "not a percentage",
			oid: &mib.OIDMetadata{
				Name: "ifSpeed",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pg.isPercentage(tt.oid)
			if got != tt.want {
				t.Errorf("isPercentage() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPanelGenerator_formatTitle(t *testing.T) {
	pg := NewPanelGenerator("snmp__")

	tests := []struct {
		name string
		oid  *mib.OIDMetadata
		want string
	}{
		{
			name: "camelCase name",
			oid: &mib.OIDMetadata{
				Name: "ifInOctets",
			},
			want: "if In Octets",
		},
		{
			name: "PascalCase name",
			oid: &mib.OIDMetadata{
				Name: "SysDescr",
			},
			want: "Sys Descr",
		},
		{
			name: "lowercase name",
			oid: &mib.OIDMetadata{
				Name: "counter",
			},
			want: "counter",
		},
		{
			name: "empty name uses OID",
			oid: &mib.OIDMetadata{
				OID:  "1.3.6.1.2.1.1.1.0",
				Name: "",
			},
			want: "1.3.6.1.2.1.1.1.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pg.formatTitle(tt.oid)
			if got != tt.want {
				t.Errorf("formatTitle() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPanelGenerator_buildThresholds(t *testing.T) {
	pg := NewPanelGenerator("snmp__")

	tests := []struct {
		name          string
		oid           *mib.OIDMetadata
		wantStepCount int
		wantMode      string
	}{
		{
			name: "percentage thresholds",
			oid: &mib.OIDMetadata{
				Name: "cpuUtilization",
			},
			wantStepCount: 3, // green, yellow, red
			wantMode:      "absolute",
		},
		{
			name: "non-percentage",
			oid: &mib.OIDMetadata{
				Name: "ifSpeed",
			},
			wantStepCount: 1, // just green
			wantMode:      "absolute",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := pg.buildThresholds(tt.oid)
			if config.Mode != tt.wantMode {
				t.Errorf("buildThresholds() Mode = %v, want %v", config.Mode, tt.wantMode)
			}
			if len(config.Steps) != tt.wantStepCount {
				t.Errorf("buildThresholds() step count = %v, want %v", len(config.Steps), tt.wantStepCount)
			}
		})
	}
}

func TestPanelGenerator_ResetPanelID(t *testing.T) {
	pg := NewPanelGenerator("snmp__")

	// Generate some panels to increment ID
	oid := &mib.OIDMetadata{
		OID:      "1.3.6.1.2.1.1.1.0",
		Name:     "sysDescr",
		Type:     "OctetString",
		IsScalar: true,
	}
	gridPos := grafana.GridPos{H: 8, W: 12, X: 0, Y: 0}

	_, _ = pg.GenerateScalarPanel(oid, gridPos)
	_, _ = pg.GenerateScalarPanel(oid, gridPos)

	if pg.nextPanelID == 1 {
		t.Error("Panel ID should have incremented")
	}

	pg.ResetPanelID()

	if pg.nextPanelID != 1 {
		t.Errorf("ResetPanelID() nextPanelID = %v, want 1", pg.nextPanelID)
	}
}

// Benchmark tests
func BenchmarkPanelGenerator_GenerateScalarPanel(b *testing.B) {
	pg := NewPanelGenerator("snmp__")
	oid := &mib.OIDMetadata{
		OID:      "1.3.6.1.2.1.1.1.0",
		Name:     "sysDescr",
		Type:     "OctetString",
		IsScalar: true,
	}
	gridPos := grafana.GridPos{H: 8, W: 12, X: 0, Y: 0}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pg.ResetPanelID()
		_, _ = pg.GenerateScalarPanel(oid, gridPos)
	}
}

func BenchmarkPanelGenerator_DeterminePanelType(b *testing.B) {
	pg := NewPanelGenerator("snmp__")
	oid := &mib.OIDMetadata{
		Type: "Counter32",
		Name: "ifInOctets",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = pg.DeterminePanelType(oid)
	}
}

func BenchmarkPanelGenerator_CalculateGridPos(b *testing.B) {
	pg := NewPanelGenerator("snmp__")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = pg.CalculateGridPos(i%10, i/10)
	}
}

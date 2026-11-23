package builder

import (
	"fmt"

	"github.com/jeremyhahn/go-netmon/internal/grafana"
	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/promname"
)

// VariableGenerator generates dashboard variables
type VariableGenerator struct {
	metricPrefix string
}

// NewVariableGenerator creates a new variable generator
func NewVariableGenerator(metricPrefix string) *VariableGenerator {
	if metricPrefix == "" {
		metricPrefix = promname.Prefix
	}
	return &VariableGenerator{
		metricPrefix: metricPrefix,
	}
}

// GenerateDeviceVariable creates a multi-select device variable
func (vg *VariableGenerator) GenerateDeviceVariable(_ string) grafana.Variable {
	// Use label_values(device) to get all devices - simpler and works in Grafana 12.x
	queryStr := "label_values(device)"

	return grafana.Variable{
		Type:  "query",
		Name:  "device",
		Label: "Device",
		DataSource: &grafana.DataSourceRef{
			Type: "prometheus",
			UID:  "prometheus",
		},
		Query: grafana.VariableQuery{
			Query: queryStr,
			RefID: "PrometheusVariableQueryEditor-VariableQuery",
		},
		Multi:      true,
		IncludeAll: true,
		AllValue:   ".*",
		Refresh:    1, // On dashboard load
		Sort:       1, // Alphabetical ascending
		Current:    &grafana.VariableOption{},
	}
}

// GenerateTableIndexVariable creates a variable for table row selection.
//
// The rows of a table are told apart by the index column's label, so the
// variable lists that label's values on the column's own metric. It takes the
// column and its index column rather than the table OID because neither the
// metric name nor the label name can be derived from an OID.
func (vg *VariableGenerator) GenerateTableIndexVariable(column *mib.OIDMetadata, indexColumnName string) grafana.Variable {
	label := promname.IndexLabel(indexColumnName)
	query := fmt.Sprintf("label_values(%s, %s)", vg.metricName(column), label)

	return grafana.Variable{
		Type:  "query",
		Name:  label,
		Label: "Index",
		DataSource: &grafana.DataSourceRef{
			Type: "prometheus",
			UID:  "prometheus",
		},
		Query:      query,
		Multi:      true,
		IncludeAll: true,
		AllValue:   ".*",
		Refresh:    1, // On dashboard load
		Sort:       1, // Alphabetical ascending
		Definition: query,
	}
}

// GenerateInterfaceVariable creates a variable for interface selection
func (vg *VariableGenerator) GenerateInterfaceVariable(_ string) grafana.Variable {
	// Query all interface names - use label_values(ifname) for simplicity
	queryStr := "label_values(ifname)"

	return grafana.Variable{
		Type:  "query",
		Name:  "interface",
		Label: "Interface",
		DataSource: &grafana.DataSourceRef{
			Type: "prometheus",
			UID:  "prometheus",
		},
		Query: grafana.VariableQuery{
			Query: queryStr,
			RefID: "PrometheusVariableQueryEditor-VariableQuery",
		},
		Multi:      true,
		IncludeAll: true,
		AllValue:   ".*",
		Refresh:    1, // On dashboard load
		Sort:       1, // Alphabetical ascending
		Current:    &grafana.VariableOption{},
	}
}

// GenerateIntervalVariable creates a time interval variable
func (vg *VariableGenerator) GenerateIntervalVariable() grafana.Variable {
	return grafana.Variable{
		Type:  "interval",
		Name:  "interval",
		Label: "Interval",
		Options: []grafana.VariableOption{
			{Text: "1m", Value: "1m", Selected: false},
			{Text: "5m", Value: "5m", Selected: true},
			{Text: "10m", Value: "10m", Selected: false},
			{Text: "30m", Value: "30m", Selected: false},
			{Text: "1h", Value: "1h", Selected: false},
		},
		Current: &grafana.VariableOption{
			Text:     "5m",
			Value:    "5m",
			Selected: true,
		},
	}
}

// GenerateCustomVariable creates a custom variable with predefined options
func (vg *VariableGenerator) GenerateCustomVariable(name, label string, options []grafana.VariableOption, multi, includeAll bool) grafana.Variable {
	var current *grafana.VariableOption
	if len(options) > 0 {
		for i := range options {
			if options[i].Selected {
				current = &options[i]
				break
			}
		}
		if current == nil {
			current = &options[0]
		}
	}

	allValue := ""
	if includeAll {
		allValue = ".*"
	}

	return grafana.Variable{
		Type:       "custom",
		Name:       name,
		Label:      label,
		Multi:      multi,
		IncludeAll: includeAll,
		AllValue:   allValue,
		Options:    options,
		Current:    current,
	}
}

// GenerateConstantVariable creates a constant variable
func (vg *VariableGenerator) GenerateConstantVariable(name, value string, hide bool) grafana.Variable {
	hideVal := 0
	if hide {
		hideVal = 2 // Hide variable
	}

	return grafana.Variable{
		Type:  "constant",
		Name:  name,
		Query: value,
		Hide:  hideVal,
		Current: &grafana.VariableOption{
			Text:     value,
			Value:    value,
			Selected: true,
		},
	}
}

// GenerateTextBoxVariable creates a text box variable
func (vg *VariableGenerator) GenerateTextBoxVariable(name, label, defaultValue string) grafana.Variable {
	return grafana.Variable{
		Type:  "textbox",
		Name:  name,
		Label: label,
		Query: defaultValue,
		Current: &grafana.VariableOption{
			Text:     defaultValue,
			Value:    defaultValue,
			Selected: true,
		},
	}
}

// GenerateAdHocVariable creates an ad-hoc variable for filtering
func (vg *VariableGenerator) GenerateAdHocVariable() grafana.Variable {
	return grafana.Variable{
		Type:  "adhoc",
		Name:  "filters",
		Label: "Filters",
		DataSource: &grafana.DataSourceRef{
			Type: "prometheus",
		},
	}
}

// metricName returns an object's metric name, the same way QueryBuilder does.
func (vg *VariableGenerator) metricName(oid *mib.OIDMetadata) string {
	return promname.NameWithPrefix(vg.metricPrefix, oid.Name)
}

// GenerateDefaultVariables creates a standard set of variables for SNMP dashboards
func (vg *VariableGenerator) GenerateDefaultVariables(sampleOID string) []grafana.Variable {
	vars := []grafana.Variable{
		vg.GenerateDeviceVariable(sampleOID),
		vg.GenerateIntervalVariable(),
	}

	return vars
}

// GenerateTableVariables creates variables for table-based dashboards
func (vg *VariableGenerator) GenerateTableVariables(sampleOID, tableOID string) []grafana.Variable {
	vars := []grafana.Variable{
		vg.GenerateDeviceVariable(sampleOID),
		vg.GenerateInterfaceVariable(tableOID),
		vg.GenerateIntervalVariable(),
	}

	return vars
}

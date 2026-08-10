// Package grafana models the Grafana dashboard JSON schema this project emits.
package grafana

import "time"

// Dashboard represents a Grafana dashboard
type Dashboard struct {
	UID           string      `json:"uid,omitempty"`
	Title         string      `json:"title"`
	Tags          []string    `json:"tags,omitempty"`
	Timezone      string      `json:"timezone"`
	SchemaVersion int         `json:"schemaVersion"`
	Version       int         `json:"version"`
	Refresh       string      `json:"refresh,omitempty"`
	Time          TimeRange   `json:"time"`
	Panels        []Panel     `json:"panels"`
	Templating    Templating  `json:"templating"`
	Annotations   Annotations `json:"annotations,omitempty"`
	Editable      bool        `json:"editable"`
	Description   string      `json:"description,omitempty"`
	GTime         *GTime      `json:"gnetId,omitempty"`
}

// TimeRange represents the dashboard time range
type TimeRange struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Templating contains dashboard variables
type Templating struct {
	List []Variable `json:"list"`
}

// Variable represents a dashboard variable
type Variable struct {
	Type           string           `json:"type"`
	Name           string           `json:"name"`
	Label          string           `json:"label,omitempty"`
	DataSource     *DataSourceRef   `json:"datasource,omitempty"`
	Query          interface{}      `json:"query,omitempty"`
	Regex          string           `json:"regex,omitempty"`
	Multi          bool             `json:"multi"`
	IncludeAll     bool             `json:"includeAll"`
	AllValue       string           `json:"allValue,omitempty"`
	Refresh        int              `json:"refresh,omitempty"`
	Options        []VariableOption `json:"options,omitempty"`
	Current        *VariableOption  `json:"current,omitempty"`
	Hide           int              `json:"hide,omitempty"`
	Definition     string           `json:"definition,omitempty"`
	Sort           int              `json:"sort,omitempty"`
	TagsQuery      string           `json:"tagsQuery,omitempty"`
	TagValuesQuery string           `json:"tagValuesQuery,omitempty"`
	UseTags        bool             `json:"useTags,omitempty"`
}

// VariableOption represents a variable option
type VariableOption struct {
	Text     string `json:"text"`
	Value    string `json:"value"`
	Selected bool   `json:"selected"`
}

// VariableQuery represents a Grafana 12.x variable query object
type VariableQuery struct {
	Query string `json:"query"`
	RefID string `json:"refId"`
}

// DataSourceRef references a Grafana datasource
type DataSourceRef struct {
	Type string `json:"type"`
	UID  string `json:"uid,omitempty"`
}

// Panel represents a dashboard panel
type Panel struct {
	ID              int                    `json:"id"`
	Type            string                 `json:"type"`
	Title           string                 `json:"title"`
	GridPos         GridPos                `json:"gridPos"`
	Targets         []Target               `json:"targets"`
	FieldConfig     FieldConfig            `json:"fieldConfig,omitempty"`
	Options         map[string]interface{} `json:"options,omitempty"`
	Description     string                 `json:"description,omitempty"`
	Transparent     bool                   `json:"transparent,omitempty"`
	Links           []PanelLink            `json:"links,omitempty"`
	Repeat          string                 `json:"repeat,omitempty"`
	RepeatDirection string                 `json:"repeatDirection,omitempty"`
	DataSource      *DataSourceRef         `json:"datasource,omitempty"`
	MaxPerRow       int                    `json:"maxPerRow,omitempty"`
}

// GridPos defines panel position and size
type GridPos struct {
	H int `json:"h"` // Height
	W int `json:"w"` // Width (1-24)
	X int `json:"x"` // X position
	Y int `json:"y"` // Y position
}

// Target represents a panel query target
type Target struct {
	Expr           string         `json:"expr"`
	RefID          string         `json:"refId"`
	LegendFormat   string         `json:"legendFormat,omitempty"`
	Interval       string         `json:"interval,omitempty"`
	Format         string         `json:"format,omitempty"`
	Instant        bool           `json:"instant,omitempty"`
	Range          bool           `json:"range,omitempty"`
	IntervalFactor int            `json:"intervalFactor,omitempty"`
	DataSource     *DataSourceRef `json:"datasource,omitempty"`
	Hide           bool           `json:"hide,omitempty"`
	Exemplar       bool           `json:"exemplar,omitempty"`
}

// FieldConfig contains field configuration
type FieldConfig struct {
	Defaults  FieldConfigDefaults   `json:"defaults,omitempty"`
	Overrides []FieldConfigOverride `json:"overrides,omitempty"`
}

// FieldConfigDefaults contains default field settings
type FieldConfigDefaults struct {
	Unit        string                 `json:"unit,omitempty"`
	Min         *float64               `json:"min,omitempty"`
	Max         *float64               `json:"max,omitempty"`
	Decimals    *int                   `json:"decimals,omitempty"`
	Color       *ColorConfig           `json:"color,omitempty"`
	Thresholds  *ThresholdsConfig      `json:"thresholds,omitempty"`
	Mappings    []ValueMapping         `json:"mappings,omitempty"`
	Links       []DataLink             `json:"links,omitempty"`
	NoValue     string                 `json:"noValue,omitempty"`
	DisplayName string                 `json:"displayName,omitempty"`
	Custom      map[string]interface{} `json:"custom,omitempty"`
}

// FieldConfigOverride allows field-specific overrides
type FieldConfigOverride struct {
	Matcher    FieldMatcher    `json:"matcher"`
	Properties []FieldProperty `json:"properties"`
}

// FieldMatcher identifies fields to override
type FieldMatcher struct {
	ID      string `json:"id"`
	Options string `json:"options,omitempty"`
}

// FieldProperty defines an override property
type FieldProperty struct {
	ID    string      `json:"id"`
	Value interface{} `json:"value"`
}

// ColorConfig defines color settings
type ColorConfig struct {
	Mode       string `json:"mode,omitempty"`
	FixedColor string `json:"fixedColor,omitempty"`
	SeriesBy   string `json:"seriesBy,omitempty"`
}

// ThresholdsConfig defines threshold settings
type ThresholdsConfig struct {
	Mode  string      `json:"mode"`
	Steps []Threshold `json:"steps"`
}

// Threshold represents a single threshold
type Threshold struct {
	Color string   `json:"color"`
	Value *float64 `json:"value"`
}

// ValueMapping maps values to text/colors
type ValueMapping struct {
	Type    string                 `json:"type"`
	Options map[string]interface{} `json:"options"`
}

// DataLink represents a data link
type DataLink struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	TargetBlank bool   `json:"targetBlank,omitempty"`
}

// PanelLink represents a panel link
type PanelLink struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	TargetBlank bool   `json:"targetBlank,omitempty"`
}

// Annotations contains dashboard annotations
type Annotations struct {
	List []Annotation `json:"list,omitempty"`
}

// Annotation represents a dashboard annotation
type Annotation struct {
	Name       string `json:"name"`
	DataSource string `json:"datasource"`
	Enable     bool   `json:"enable"`
	IconColor  string `json:"iconColor,omitempty"`
	Query      string `json:"query,omitempty"`
}

// GTime represents Grafana time
type GTime struct {
	ID   int       `json:"id,omitempty"`
	Time time.Time `json:"time,omitempty"`
}

// Panel type constants
const (
	PanelTypeTimeSeries = "timeseries"
	PanelTypeStat       = "stat"
	PanelTypeGauge      = "gauge"
	PanelTypeTable      = "table"
	PanelTypeRow        = "row"
	PanelTypeText       = "text"
	PanelTypeHeatmap    = "heatmap"
	PanelTypeGraph      = "graph" // Legacy
)

// Variable type constants
const (
	VariableTypeQuery    = "query"
	VariableTypeCustom   = "custom"
	VariableTypeConstant = "constant"
	VariableTypeInterval = "interval"
	VariableTypeTextbox  = "textbox"
)

// Color mode constants
const (
	ColorModePalette = "palette-classic"
	ColorModeValue   = "value"
	ColorModeFixed   = "fixed"
)

// Threshold mode constants
const (
	ThresholdModeAbsolute   = "absolute"
	ThresholdModePercentage = "percentage"
)

// NewDashboard creates a new dashboard with sensible defaults
func NewDashboard(uid, title string) *Dashboard {
	return &Dashboard{
		UID:           uid,
		Title:         title,
		Tags:          []string{},
		Timezone:      "browser",
		Editable:      true,
		Refresh:       "30s",
		SchemaVersion: 36,
		Time: TimeRange{
			From: "now-6h",
			To:   "now",
		},
		Templating: Templating{
			List: []Variable{},
		},
		Panels: []Panel{},
	}
}

// AddPanel adds a panel to the dashboard
func (d *Dashboard) AddPanel(panel Panel) {
	d.Panels = append(d.Panels, panel)
}

// AddVariable adds a template variable to the dashboard
func (d *Dashboard) AddVariable(variable Variable) {
	d.Templating.List = append(d.Templating.List, variable)
}

// NewTimeSeriesPanel creates a new time series panel
func NewTimeSeriesPanel(id int, title string, gridPos GridPos) Panel {
	return Panel{
		ID:      id,
		Type:    PanelTypeTimeSeries,
		Title:   title,
		GridPos: gridPos,
		Targets: []Target{},
		FieldConfig: FieldConfig{
			Defaults: FieldConfigDefaults{
				Custom: map[string]interface{}{
					"drawStyle":         "line",
					"lineInterpolation": "linear",
					"lineWidth":         1,
					"fillOpacity":       0,
					"gradientMode":      "none",
					"showPoints":        "auto",
					"pointSize":         5,
					"stacking": map[string]interface{}{
						"mode":  "none",
						"group": "A",
					},
					"axisPlacement": "auto",
					"axisLabel":     "",
					"scaleDistribution": map[string]interface{}{
						"type": "linear",
					},
					"hideFrom": map[string]interface{}{
						"tooltip": false,
						"viz":     false,
						"legend":  false,
					},
					"thresholdsStyle": map[string]interface{}{
						"mode": "off",
					},
				},
			},
			Overrides: []FieldConfigOverride{},
		},
		Options: map[string]interface{}{
			"tooltip": map[string]interface{}{
				"mode": "single",
				"sort": "none",
			},
			"legend": map[string]interface{}{
				"showLegend":  true,
				"displayMode": "list",
				"placement":   "bottom",
				"calcs":       []string{},
			},
		},
	}
}

// NewStatPanel creates a new stat panel
func NewStatPanel(id int, title string, gridPos GridPos) Panel {
	return Panel{
		ID:      id,
		Type:    PanelTypeStat,
		Title:   title,
		GridPos: gridPos,
		Targets: []Target{},
		FieldConfig: FieldConfig{
			Defaults: FieldConfigDefaults{
				Color: &ColorConfig{
					Mode: ColorModeValue,
				},
			},
			Overrides: []FieldConfigOverride{},
		},
		Options: map[string]interface{}{
			"reduceOptions": map[string]interface{}{
				"values": false,
				"calcs":  []string{"lastNotNull"},
				"fields": "",
			},
			"orientation":       "auto",
			"textMode":          "auto",
			"colorMode":         "value",
			"graphMode":         "area",
			"justifyMode":       "auto",
			"showPercentChange": false,
		},
	}
}

// NewGaugePanel creates a new gauge panel
func NewGaugePanel(id int, title string, gridPos GridPos) Panel {
	lower := 0.0
	upper := 100.0
	return Panel{
		ID:      id,
		Type:    PanelTypeGauge,
		Title:   title,
		GridPos: gridPos,
		Targets: []Target{},
		FieldConfig: FieldConfig{
			Defaults: FieldConfigDefaults{
				Min: &lower,
				Max: &upper,
			},
			Overrides: []FieldConfigOverride{},
		},
		Options: map[string]interface{}{
			"reduceOptions": map[string]interface{}{
				"values": false,
				"calcs":  []string{"lastNotNull"},
				"fields": "",
			},
			"orientation":          "auto",
			"showThresholdLabels":  false,
			"showThresholdMarkers": true,
		},
	}
}

// NewTablePanel creates a new table panel
func NewTablePanel(id int, title string, gridPos GridPos) Panel {
	return Panel{
		ID:      id,
		Type:    PanelTypeTable,
		Title:   title,
		GridPos: gridPos,
		Targets: []Target{},
		FieldConfig: FieldConfig{
			Defaults: FieldConfigDefaults{
				Custom: map[string]interface{}{
					"align":       "auto",
					"displayMode": "auto",
					"filterable":  false,
				},
			},
			Overrides: []FieldConfigOverride{},
		},
		Options: map[string]interface{}{
			"showHeader": true,
			"footer": map[string]interface{}{
				"show":             false,
				"reducer":          []string{"sum"},
				"fields":           "",
				"enablePagination": false,
			},
		},
	}
}

// NewRowPanel creates a new row panel for grouping
func NewRowPanel(id int, title string, gridPos GridPos) Panel {
	return Panel{
		ID:      id,
		Type:    PanelTypeRow,
		Title:   title,
		GridPos: gridPos,
	}
}

// NewQueryVariable creates a new query-based template variable
func NewQueryVariable(name, label, query string) Variable {
	return Variable{
		Name:       name,
		Type:       VariableTypeQuery,
		Label:      label,
		DataSource: &DataSourceRef{Type: "prometheus"},
		Query:      query,
		Refresh:    1, // On dashboard load
		Multi:      true,
		IncludeAll: true,
		Sort:       1, // Alphabetical
	}
}

// NewCustomVariable creates a new custom template variable
func NewCustomVariable(name, label string, options []string) Variable {
	opts := make([]VariableOption, len(options))
	for i, opt := range options {
		opts[i] = VariableOption{
			Text:  opt,
			Value: opt,
		}
	}
	return Variable{
		Name:    name,
		Type:    VariableTypeCustom,
		Label:   label,
		Options: opts,
		Multi:   false,
	}
}

// AddTarget adds a query target to the panel
func (p *Panel) AddTarget(target Target) {
	p.Targets = append(p.Targets, target)
}

// NewTarget creates a new query target
func NewTarget(refID, expr, legendFormat string) Target {
	return Target{
		RefID:        refID,
		Expr:         expr,
		LegendFormat: legendFormat,
		Range:        true,
	}
}

// NewInstantTarget creates a new instant query target
func NewInstantTarget(refID, expr, legendFormat string) Target {
	return Target{
		RefID:        refID,
		Expr:         expr,
		LegendFormat: legendFormat,
		Instant:      true,
	}
}

// SetUnit sets the unit for the panel's field config
func (p *Panel) SetUnit(unit string) {
	p.FieldConfig.Defaults.Unit = unit
}

// SetThresholds sets thresholds for the panel
func (p *Panel) SetThresholds(mode string, steps []Threshold) {
	p.FieldConfig.Defaults.Thresholds = &ThresholdsConfig{
		Mode:  mode,
		Steps: steps,
	}
}

// SetMinMax sets min and max values for the panel
func (p *Panel) SetMinMax(lower, upper float64) {
	p.FieldConfig.Defaults.Min = &lower
	p.FieldConfig.Defaults.Max = &upper
}

// SetDecimals sets the number of decimal places
func (p *Panel) SetDecimals(decimals int) {
	p.FieldConfig.Defaults.Decimals = &decimals
}

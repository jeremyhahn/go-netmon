package template

import "time"

// Dashboard represents a complete Grafana dashboard
type Dashboard struct {
	UID           string       `json:"uid"`
	Title         string       `json:"title"`
	Description   string       `json:"description,omitempty"`
	Tags          []string     `json:"tags,omitempty"`
	Timezone      string       `json:"timezone"`
	Editable      bool         `json:"editable"`
	SchemaVersion int          `json:"schemaVersion"`
	Templating    *Templating  `json:"templating"`
	Panels        []Panel      `json:"panels"`
	Refresh       string       `json:"refresh"`
	Time          *TimeRange   `json:"time"`
	Version       int          `json:"version"`
	Annotations   *Annotations `json:"annotations,omitempty"`
}

// Templating contains dashboard variables
type Templating struct {
	List []Variable `json:"list"`
}

// Variable represents a dashboard variable
type Variable struct {
	Name       string      `json:"name"`
	Type       string      `json:"type"`
	Datasource *Datasource `json:"datasource,omitempty"`
	Query      string      `json:"query,omitempty"`
	Multi      bool        `json:"multi"`
	IncludeAll bool        `json:"includeAll"`
	Current    *VarCurrent `json:"current,omitempty"`
	Label      string      `json:"label,omitempty"`
	Hide       int         `json:"hide,omitempty"`
	AllValue   string      `json:"allValue,omitempty"`
	Refresh    int         `json:"refresh,omitempty"`
	Regex      string      `json:"regex,omitempty"`
}

// VarCurrent represents the current value of a variable
type VarCurrent struct {
	Selected bool   `json:"selected"`
	Text     string `json:"text"`
	Value    string `json:"value"`
}

// TimeRange represents the dashboard time range
type TimeRange struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Panel represents a dashboard panel
type Panel struct {
	ID          int          `json:"id"`
	Type        string       `json:"type"`
	Title       string       `json:"title"`
	Description string       `json:"description,omitempty"`
	GridPos     *GridPos     `json:"gridPos"`
	Targets     []Target     `json:"targets,omitempty"`
	Options     interface{}  `json:"options,omitempty"`
	FieldConfig *FieldConfig `json:"fieldConfig,omitempty"`
	Datasource  *Datasource  `json:"datasource,omitempty"`
}

// GridPos defines panel position and size
type GridPos struct {
	H int `json:"h"`
	W int `json:"w"`
	X int `json:"x"`
	Y int `json:"y"`
}

// Target represents a query target
type Target struct {
	Expr           string      `json:"expr"`
	RefID          string      `json:"refId"`
	LegendFormat   string      `json:"legendFormat,omitempty"`
	Interval       string      `json:"interval,omitempty"`
	Format         string      `json:"format,omitempty"`
	Instant        bool        `json:"instant,omitempty"`
	Range          bool        `json:"range,omitempty"`
	IntervalFactor int         `json:"intervalFactor,omitempty"`
	Datasource     *Datasource `json:"datasource,omitempty"`
}

// FieldConfig represents field configuration
type FieldConfig struct {
	Defaults  *FieldDefaults  `json:"defaults,omitempty"`
	Overrides []FieldOverride `json:"overrides,omitempty"`
}

// FieldDefaults represents default field settings
type FieldDefaults struct {
	Color      *ColorConfig   `json:"color,omitempty"`
	Unit       string         `json:"unit,omitempty"`
	Min        *float64       `json:"min,omitempty"`
	Max        *float64       `json:"max,omitempty"`
	Decimals   *int           `json:"decimals,omitempty"`
	Thresholds *Thresholds    `json:"thresholds,omitempty"`
	Mappings   []ValueMapping `json:"mappings,omitempty"`
	Custom     interface{}    `json:"custom,omitempty"`
}

// ColorConfig represents color configuration
type ColorConfig struct {
	Mode       string `json:"mode"`
	FixedColor string `json:"fixedColor,omitempty"`
}

// Thresholds represents threshold configuration
type Thresholds struct {
	Mode  string      `json:"mode"`
	Steps []Threshold `json:"steps"`
}

// Threshold represents a single threshold
type Threshold struct {
	Color string   `json:"color"`
	Value *float64 `json:"value"`
}

// ValueMapping represents a value mapping
type ValueMapping struct {
	Type    string                 `json:"type"`
	Options map[string]interface{} `json:"options"`
}

// FieldOverride represents field override configuration
type FieldOverride struct {
	Matcher    map[string]interface{}   `json:"matcher"`
	Properties []map[string]interface{} `json:"properties"`
}

// Datasource represents a datasource reference
type Datasource struct {
	Type string `json:"type"`
	UID  string `json:"uid,omitempty"`
}

// Annotations represents dashboard annotations
type Annotations struct {
	List []Annotation `json:"list"`
}

// Annotation represents a single annotation
type Annotation struct {
	Name       string      `json:"name"`
	Datasource *Datasource `json:"datasource"`
	Enable     bool        `json:"enable"`
	Hide       bool        `json:"hide"`
	IconColor  string      `json:"iconColor"`
	Type       string      `json:"type"`
}

// IndexEntry represents a dashboard in the index
type IndexEntry struct {
	UID       string    `json:"uid"`
	Title     string    `json:"title"`
	Module    string    `json:"module"`
	Panels    int       `json:"panels"`
	Generated time.Time `json:"generated"`
}

// Index represents the dashboard catalog
type Index struct {
	Dashboards []IndexEntry `json:"dashboards"`
}

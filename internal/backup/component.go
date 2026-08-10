package backup

import "strings"

// Component is one independently selectable part of a deployment's state.
type Component string

// The components a backup can carry, in the order a restore applies them.
//
// Host-side content is restored before the volumes so that the sources are in
// place by the time the stack comes back up and re-reads them.
const (
	// ComponentConfig covers the repository files bind-mounted into the stack.
	ComponentConfig Component = "config"
	// ComponentMIBs covers the MIB source tree, a host directory bind-mounted
	// read-only into the poller and the web UI. It is not a Docker volume.
	ComponentMIBs Component = "mibs"
	// ComponentWebUI covers the web UI's records and MIB catalog.
	ComponentWebUI Component = "webui"
	// ComponentPoller covers the poller's MIB metadata store and uploaded MIBs.
	ComponentPoller Component = "poller"
	// ComponentMetrics covers the Prometheus TSDB.
	ComponentMetrics Component = "metrics"
	// ComponentGrafana covers the Grafana database and dashboards.
	ComponentGrafana Component = "grafana"
)

// AllComponents lists every component, in restore order.
func AllComponents() []Component {
	return []Component{
		ComponentConfig,
		ComponentMIBs,
		ComponentWebUI,
		ComponentPoller,
		ComponentMetrics,
		ComponentGrafana,
	}
}

// ConfigFiles lists the repository files the config component carries.
//
// They are data in the operational sense: the stack behaves differently if they
// change.
func ConfigFiles() []string { return []string{"config.yaml", "prometheus.yml"} }

// ReferenceFiles lists files recorded in a backup for provenance only.
//
// They are never restored. Overwriting the deployment definition beneath a
// running deployment is a deployment action, not a data restore.
func ReferenceFiles() []string { return []string{"docker-compose.yml"} }

// Volume reports the compose volume a component's data lives in, or the empty
// string for components that cover host-side content.
func (c Component) Volume() string {
	switch c {
	case ComponentWebUI:
		return "webui-data"
	case ComponentPoller:
		return "go-netmon-data"
	case ComponentMetrics:
		return "prometheus-data"
	case ComponentGrafana:
		return "grafana-data"
	case ComponentConfig, ComponentMIBs:
		return ""
	default:
		return ""
	}
}

// Description explains, in operator terms, what a component holds. A plan that
// destroys data must say what is being destroyed, not just name a volume.
func (c Component) Description() string {
	switch c {
	case ComponentConfig:
		return "bind-mounted repository config (" + strings.Join(ConfigFiles(), ", ") + ")"
	case ComponentMIBs:
		return "MIB source tree bind-mounted read-only into the poller and web UI"
	case ComponentWebUI:
		return "devices, profiles, MIB assignments, poll intervals, MIB catalog"
	case ComponentPoller:
		return "poller MIB metadata store and uploaded MIB files"
	case ComponentMetrics:
		return "Prometheus TSDB - all metric history"
	case ComponentGrafana:
		return "Grafana database and dashboards"
	default:
		return string(c)
	}
}

// Valid reports whether the name is a known component.
func (c Component) Valid() bool {
	for _, known := range AllComponents() {
		if known == c {
			return true
		}
	}
	return false
}

// ParseComponents turns a comma-separated list into components, refusing any
// name this tool does not know.
//
// An unknown name is fatal rather than ignored: silently dropping a typo in
// --only would back up, or restore, something other than what was asked for.
func ParseComponents(list string) ([]Component, error) {
	var out []Component
	for _, raw := range strings.Split(list, ",") {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		c := Component(name)
		if !c.Valid() {
			return nil, &UnknownComponentError{Name: name, Valid: AllComponents()}
		}
		out = append(out, c)
	}
	return out, nil
}

// selectComponents resolves an only/skip pair against the components available,
// preserving the canonical restore order.
func selectComponents(available, only, skip []Component) ([]Component, error) {
	if len(only) > 0 && len(skip) > 0 {
		return nil, &SelectionConflictError{Reason: "--only and --skip are mutually exclusive"}
	}
	if len(only) > 0 {
		for _, want := range only {
			if !containsComponent(available, want) {
				return nil, &ComponentAbsentError{Name: want, Available: available}
			}
		}
		return orderComponents(available, only), nil
	}
	var kept []Component
	for _, c := range available {
		if !containsComponent(skip, c) {
			kept = append(kept, c)
		}
	}
	if len(kept) == 0 {
		return nil, &EmptySelectionError{}
	}
	return kept, nil
}

// orderComponents returns the wanted components in the order they appear in
// available.
func orderComponents(available, wanted []Component) []Component {
	out := make([]Component, 0, len(wanted))
	for _, c := range available {
		if containsComponent(wanted, c) {
			out = append(out, c)
		}
	}
	return out
}

func containsComponent(list []Component, want Component) bool {
	for _, c := range list {
		if c == want {
			return true
		}
	}
	return false
}

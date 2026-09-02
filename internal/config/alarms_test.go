package config

import (
	"strings"
	"testing"
)

// Validating the alarm rules an operator writes.
//
// The check is deliberately shallow -- it does not parse the PromQL, because
// this process does not evaluate it -- so what it has to catch is the shape.
// Every case here is a rule that would be accepted, loaded, and then quietly do
// nothing or do the wrong thing.

func TestAlarmRulesAreValidated(t *testing.T) {
	tests := []struct {
		name  string
		rules []AlarmRule
		field string
	}{
		{
			name:  "a rule with no name cannot be shown",
			rules: []AlarmRule{{Expr: "up == 0", Severity: "critical"}},
			field: "alarms[0].name",
		},
		{
			name: "a rule with no expression can never fire",
			rules: []AlarmRule{
				{Name: "Down", Severity: "critical"},
			},
			field: "alarms[0].expr",
		},
		{
			// A severity nobody recognizes sorts to the bottom of the bell, so
			// a critical alarm written "CRITICAL" would sit under the warnings.
			name: "a severity nothing orders",
			rules: []AlarmRule{
				{Name: "Down", Expr: "up == 0", Severity: "CRITICAL"},
			},
			field: "alarms[0].severity",
		},
		{
			name: "a for that is not a duration",
			rules: []AlarmRule{
				{Name: "Down", Expr: "up == 0", Severity: "critical", For: "5 minutes"},
			},
			field: "alarms[0].for",
		},
		{
			// Two rules with one name are one row in the bell, and which of
			// them it is depends on evaluation order.
			name: "two rules with the same name",
			rules: []AlarmRule{
				{Name: "Down", Expr: "up == 0", Severity: "critical"},
				{Name: "Down", Expr: "other == 0", Severity: "warning"},
			},
			field: "alarms[1].name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAlarms(tt.rules)
			if err == nil {
				t.Fatalf("the rule was accepted; it would load and do nothing useful")
			}

			var invalid *ValidationError
			if !asValidationError(err, &invalid) {
				t.Fatalf("err = %v (%T), want *ValidationError", err, err)
			}
			if invalid.Field != tt.field {
				t.Errorf("field = %q, want %q", invalid.Field, tt.field)
			}
		})
	}
}

// TestAWellFormedRuleIsAccepted keeps the validation from rejecting the shapes
// the shipped configuration uses.
func TestAWellFormedRuleIsAccepted(t *testing.T) {
	rules := []AlarmRule{
		{Name: "DeviceUnreachable", Expr: "gonetmon_device_unreachable == 1",
			Severity: "critical", For: "2m", Summary: "A device stopped answering"},
		// No "for" at all: raise on the first evaluation.
		{Name: "ExportStalled", Expr: "gonetmon_export_stalled == 1", Severity: "warning"},
		// A multi-line expression, which is how the shipped rules are written.
		{Name: "InterfaceDown", Severity: "info",
			Expr: "snmp_ifOperStatus == 2\nand on(device,ifindex) snmp_ifAdminStatus == 1"},
	}

	if err := validateAlarms(rules); err != nil {
		t.Errorf("a well-formed rule set was rejected: %v", err)
	}
}

// TestNoAlarmsIsNotAnError covers the ordinary case of an operator who has not
// written any yet.
func TestNoAlarmsIsNotAnError(t *testing.T) {
	if err := validateAlarms(nil); err != nil {
		t.Errorf("an empty rule set was rejected: %v", err)
	}
}

// asValidationError is errors.As without the import, since ValidationError is
// returned as a concrete pointer throughout this package.
func asValidationError(err error, target **ValidationError) bool {
	invalid, ok := err.(*ValidationError)
	if ok {
		*target = invalid
	}
	return ok
}

// TestTheShippedRulesLoad reads the example configuration rather than a
// fixture, so a rule that ships broken fails here rather than on the appliance.
func TestTheShippedRulesLoad(t *testing.T) {
	for _, path := range []string{"../../config.example.yaml", "../../deploy/config.yaml"} {
		t.Run(path, func(t *testing.T) {
			cfg, err := LoadFromFile(path)
			if err != nil {
				t.Fatalf("the shipped configuration does not load: %v", err)
			}
			if len(cfg.Alarms) == 0 {
				t.Fatal("the shipped configuration defines no alarms, so nothing " +
					"demonstrates the feature to whoever reads it first")
			}
			for _, rule := range cfg.Alarms {
				if rule.Summary == "" {
					t.Errorf("%s has no summary; the bell would show a name and "+
						"nothing else", rule.Name)
				}
				if !strings.Contains(rule.Expr, "==") && !strings.Contains(rule.Expr, "!=") &&
					!strings.Contains(rule.Expr, ">") && !strings.Contains(rule.Expr, "<") {
					t.Errorf("%s has no comparison in it, so it fires whenever the "+
						"metric exists: %q", rule.Name, rule.Expr)
				}
			}
		})
	}
}

package alarm

import (
	"time"

	"github.com/jeremyhahn/go-netmon/internal/config"
)

// RulesFrom converts validated configuration into rules; it cannot fail.
func RulesFrom(configured []config.AlarmRule) []Rule {
	rules := make([]Rule, 0, len(configured))

	for _, c := range configured {
		rule := Rule{
			Name:        c.Name,
			Expr:        c.Expr,
			Severity:    Severity(c.Severity),
			Summary:     c.Summary,
			Description: c.Description,
		}
		// Validation has already accepted this, so a parse error here is not
		// reachable from a loaded config; zero is the documented meaning of an
		// absent "for" either way.
		if c.For != "" {
			if held, err := time.ParseDuration(c.For); err == nil {
				rule.For = held
			}
		}
		rules = append(rules, rule)
	}
	return rules
}

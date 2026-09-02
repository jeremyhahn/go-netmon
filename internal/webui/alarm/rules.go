package alarm

import (
	"time"

	"github.com/jeremyhahn/go-netmon/internal/config"
)

// RulesFrom converts the operator's configuration into rules.
//
// The configuration has already been validated -- a rule with no name, no
// expression, an unrecognized severity or an unparseable duration never gets
// this far -- so this cannot fail. Keeping the two apart is what lets a reload
// reject a bad file whole, before any of it reaches the running evaluator: a
// config that half-applies is worse than one that does not apply at all,
// because the half that took effect is invisible.
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

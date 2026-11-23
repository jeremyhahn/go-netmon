package poller

import (
	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/mib/selector"
)

// selectModuleOIDs picks the objects to poll for a device that has explicit MIB
// modules assigned.
//
// The rules live in internal/mib/selector because the web UI has to be able to
// answer "is this object being polled, and if not why not?" for the same device
// and the same module. Two implementations of that question would eventually
// give two answers, and the one the operator reads would be the wrong one, so
// there is exactly one: selector.Polled is selector.Plan with the rejected
// objects dropped, and the API renders the very same plan.
func selectModuleOIDs(allOIDs []*mib.OIDMetadata, device *config.Device) []*mib.OIDMetadata {
	return selector.Polled(allOIDs, device.Modules, device.ModuleFilters)
}

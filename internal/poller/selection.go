package poller

import (
	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/mib/selector"
)

// selectModuleOIDs picks the objects to poll for a device's assigned modules, via selector.Polled.
func selectModuleOIDs(allOIDs []*mib.OIDMetadata, device *config.Device) []*mib.OIDMetadata {
	return selector.Polled(allOIDs, device.Modules, device.ModuleFilters)
}

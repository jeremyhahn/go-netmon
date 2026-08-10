// Package provider adapts the MIB loader and store to the interfaces the web UI
// API expects.
package provider

import (
	"fmt"
	"log"

	"github.com/jeremyhahn/go-netmon/internal/mib/loader"
	"github.com/jeremyhahn/go-netmon/internal/mib/storage"
	"github.com/jeremyhahn/go-netmon/internal/webui/api"
)

// MIBProvider implements the api.MIBProvider interface using the MIB loader
type MIBProvider struct {
	loader  *loader.Loader
	storage *storage.Storage
}

// NewMIBProvider creates a new MIB provider
func NewMIBProvider(l *loader.Loader, s *storage.Storage) *MIBProvider {
	return &MIBProvider{
		loader:  l,
		storage: s,
	}
}

// ListMIBs returns a list of all available MIBs
func (p *MIBProvider) ListMIBs() ([]api.MIBInfo, error) {
	modules, err := p.storage.ListModules()
	if err != nil {
		return nil, fmt.Errorf("failed to list modules: %w", err)
	}

	// Get all OIDs once to avoid multiple storage calls
	allOIDs, err := p.storage.ListOIDs("")
	if err != nil {
		return nil, fmt.Errorf("failed to list OIDs: %w", err)
	}

	// Count OIDs per module
	oidCounts := make(map[string]int)
	for _, oid := range allOIDs {
		oidCounts[oid.Module]++
	}

	mibs := make([]api.MIBInfo, 0, len(modules))
	for _, module := range modules {
		mibs = append(mibs, api.MIBInfo{
			Name:        module.Name,
			Description: module.Description,
			ObjectCount: oidCounts[module.Name],
		})
	}

	return mibs, nil
}

// GetMIB returns detailed information about a specific MIB
func (p *MIBProvider) GetMIB(name string) (*api.MIBDetail, error) {
	// Get module info
	module, err := p.storage.GetModule(name)
	if err != nil {
		return nil, fmt.Errorf("module not found: %w", err)
	}

	// Get all OIDs (not just pollable) for this module
	moduleOIDs, err := p.storage.ListModuleOIDs(name)
	if err != nil {
		return nil, fmt.Errorf("failed to list OIDs: %w", err)
	}

	log.Printf("DEBUG GetMIB(%s): Found %d OIDs in storage", name, len(moduleOIDs))

	// Convert to API format
	objects := make([]api.MIBObject, 0, len(moduleOIDs))
	for _, oid := range moduleOIDs {
		objects = append(objects, api.MIBObject{
			OID:         oid.OID,
			Name:        oid.Name,
			Type:        oid.Type,
			Description: oid.Description,
			Unit:        oid.Units,
		})
	}

	log.Printf("DEBUG GetMIB(%s): Returning %d objects", name, len(objects))

	return &api.MIBDetail{
		Name:        module.Name,
		Description: module.Description,
		Objects:     objects,
	}, nil
}

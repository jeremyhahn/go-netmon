// Command check-oid prints the stored metadata for a single OID, for
// inspecting what an import actually put in the MIB store.
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/jeremyhahn/go-netmon/internal/mib/storage"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatal("Usage: check-oid <oid>")
	}
	if err := run(os.Args[1]); err != nil {
		log.Fatal(err)
	}
}

// run is separate from main so that the deferred close still runs when the
// lookup fails; log.Fatal in main would skip it.
func run(oid string) error {
	store, err := storage.New("/app/data/mib-storage")
	if err != nil {
		return fmt.Errorf("failed to open storage: %w", err)
	}
	// Read-only use: closing has nothing left to flush.
	defer func() { _ = store.Close() }()

	metadata, err := store.GetOID(oid)
	if err != nil {
		return fmt.Errorf("failed to get OID %s: %w", oid, err)
	}

	fmt.Printf("OID: %s\n", metadata.OID)
	fmt.Printf("Name: %s\n", metadata.Name)
	fmt.Printf("Module: %s\n", metadata.Module)
	fmt.Printf("Type: %s\n", metadata.Type)
	fmt.Printf("IsCounter(): %v\n", metadata.IsCounter())
	fmt.Printf("IsGauge(): %v\n", metadata.IsGauge())
	fmt.Printf("IsColumn: %v\n", metadata.IsColumn)
	fmt.Printf("IsScalar: %v\n", metadata.IsScalar)
	fmt.Printf("Pollable: %v\n", metadata.Pollable)

	return nil
}

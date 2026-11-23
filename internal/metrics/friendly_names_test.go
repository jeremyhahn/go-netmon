package metrics

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/mib/storage"
	"github.com/jeremyhahn/go-netmon/internal/snmp"
)

// The poller retires a device's names through this capability.
var _ DeviceForgetter = (*PrometheusExporter)(nil)

// newIfDescrCache returns a cache over an ifTable whose ifDescr is a status
// column, so ifDescr names the rows of ifInOctets.
func newIfDescrCache(t *testing.T) *TableIndexCache {
	t.Helper()
	store, err := storage.New(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	objects := []*mib.OIDMetadata{
		{OID: "1.3.6.1.2.1.2.2.1", Name: "ifEntry", Module: "IF-MIB", Type: "SEQUENCE",
			IsTableEntry: true, Index: []string{"ifIndex"}, Parent: "1.3.6.1.2.1.2.2"},
		{OID: "1.3.6.1.2.1.2.2.1.1", Name: "ifIndex", Module: "IF-MIB", Type: "Integer32",
			IsColumn: true, Parent: "1.3.6.1.2.1.2.2.1", ObjectClass: mib.ObjectClassIndexColumn},
		{OID: "1.3.6.1.2.1.2.2.1.2", Name: "ifDescr", Module: "IF-MIB", Type: "DisplayString",
			IsColumn: true, Parent: "1.3.6.1.2.1.2.2.1", ObjectClass: mib.ObjectClassStatusColumn,
			TableOID: "1.3.6.1.2.1.2.2", EntryOID: "1.3.6.1.2.1.2.2.1"},
		{OID: "1.3.6.1.2.1.2.2.1.10", Name: "ifInOctets", Module: "IF-MIB", Type: "Counter32",
			IsColumn: true, Parent: "1.3.6.1.2.1.2.2.1", ObjectClass: mib.ObjectClassMetricColumn,
			TableOID: "1.3.6.1.2.1.2.2", EntryOID: "1.3.6.1.2.1.2.2.1"},
	}
	for _, object := range objects {
		if err := store.StoreOID(object); err != nil {
			t.Fatalf("StoreOID %s: %v", object.Name, err)
		}
	}
	return NewTableIndexCache(store)
}

// ifDescrPoll is one poll of an ifTable: ifDescr for each named row.
func ifDescrPoll(rows map[int]string) []*snmp.Result {
	results := make([]*snmp.Result, 0, len(rows))
	for index, descr := range rows {
		results = append(results, &snmp.Result{
			OID:       fmt.Sprintf("1.3.6.1.2.1.2.2.1.2.%d", index),
			Type:      gosnmp.OctetString,
			Value:     []byte(descr),
			Timestamp: time.Now(),
		})
	}
	return results
}

// ifDescrLabel is the ifdescr label a sample of ifInOctets.<index> carries.
func ifDescrLabel(cache *TableIndexCache, device string, index int) (string, bool) {
	label, ok := cache.ExtractIndexLabels(device, fmt.Sprintf("1.3.6.1.2.1.2.2.1.10.%d", index))["ifdescr"]
	return label, ok
}

// A row that disappears from the table loses its name with the next poll,
// rather than lending it to whatever row later takes its index.
func TestFriendlyNamesRemovedRowIsForgotten(t *testing.T) {
	cache := newIfDescrCache(t)

	cache.CacheFriendlyNames("router1", ifDescrPoll(map[int]string{1: "Gi0/0", 2: "Gi0/1"}))
	if got, _ := ifDescrLabel(cache, "router1", 2); got != "Gi0/1" {
		t.Fatalf("row 2 named %q before removal, want Gi0/1", got)
	}

	cache.CacheFriendlyNames("router1", ifDescrPoll(map[int]string{1: "Gi0/0"}))
	if got, ok := ifDescrLabel(cache, "router1", 2); ok {
		t.Errorf("row 2 is gone from the table but still named %q", got)
	}
	if got, _ := ifDescrLabel(cache, "router1", 1); got != "Gi0/0" {
		t.Errorf("row 1 named %q, want Gi0/0", got)
	}
}

// A reused index carries the name the new row reports, never the old one.
func TestFriendlyNamesReusedIndexTakesTheNewName(t *testing.T) {
	cache := newIfDescrCache(t)

	cache.CacheFriendlyNames("router1", ifDescrPoll(map[int]string{7: "Gi0/7"}))
	cache.CacheFriendlyNames("router1", ifDescrPoll(map[int]string{7: "Te1/0/1"}))

	if got, _ := ifDescrLabel(cache, "router1", 7); got != "Te1/0/1" {
		t.Errorf("reused index 7 named %q, want Te1/0/1", got)
	}
}

// A poll that walks no table with status columns leaves the device's names
// alone: only the tables a poll walked are replaced.
func TestFriendlyNamesUnwalkedTableKeepsItsNames(t *testing.T) {
	cache := newIfDescrCache(t)

	cache.CacheFriendlyNames("router1", ifDescrPoll(map[int]string{1: "Gi0/0"}))
	cache.CacheFriendlyNames("router1", []*snmp.Result{
		{OID: "1.3.6.1.2.1.1.3.0", Type: gosnmp.TimeTicks, Value: uint32(5), Timestamp: time.Now()},
	})

	if got, _ := ifDescrLabel(cache, "router1", 1); got != "Gi0/0" {
		t.Errorf("row 1 named %q after a poll that did not walk ifTable, want Gi0/0", got)
	}
}

// A device the poller retires loses every name, and no other device does.
func TestFriendlyNamesForgetDevice(t *testing.T) {
	cache := newIfDescrCache(t)

	cache.CacheFriendlyNames("router1", ifDescrPoll(map[int]string{1: "Gi0/0"}))
	cache.CacheFriendlyNames("router2", ifDescrPoll(map[int]string{1: "eth0"}))

	cache.ForgetDevice("router1")

	if got, ok := ifDescrLabel(cache, "router1", 1); ok {
		t.Errorf("removed device still names row 1 %q", got)
	}
	if _, held := cache.friendly.Load("router1"); held {
		t.Error("removed device still has a slot in the cache")
	}
	if got, _ := ifDescrLabel(cache, "router2", 1); got != "eth0" {
		t.Errorf("router2 row 1 named %q after router1 was forgotten, want eth0", got)
	}
}

// Polls and reads of different devices run concurrently; run under -race.
// Each device reads back only its own names.
func TestFriendlyNamesConcurrentDevices(t *testing.T) {
	cache := newIfDescrCache(t)

	const devices, polls = 8, 50
	var wg sync.WaitGroup
	errs := make(chan error, devices*polls)
	for d := 0; d < devices; d++ {
		device := fmt.Sprintf("router%d", d)
		wg.Add(2)
		go func() {
			defer wg.Done()
			for p := 0; p < polls; p++ {
				cache.CacheFriendlyNames(device, ifDescrPoll(map[int]string{
					1: fmt.Sprintf("%s-a-%d", device, p),
					2: fmt.Sprintf("%s-b-%d", device, p),
				}))
			}
		}()
		go func() {
			defer wg.Done()
			for p := 0; p < polls; p++ {
				if got, ok := ifDescrLabel(cache, device, 1); ok && !strings.HasPrefix(got, device+"-") {
					errs <- fmt.Errorf("%s row 1 named %q, another device's name", device, got)
				}
				if p%10 == 9 {
					cache.ForgetDevice(device + "-gone")
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	for d := 0; d < devices; d++ {
		device := fmt.Sprintf("router%d", d)
		want := fmt.Sprintf("%s-b-%d", device, polls-1)
		if got, _ := ifDescrLabel(cache, device, 2); got != want {
			t.Errorf("%s row 2 named %q after its last poll, want %q", device, got, want)
		}
	}
}

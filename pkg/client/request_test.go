package client

import (
	"context"
	"errors"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestAccessorsReturnNilOnFailure covers the error arm of every accessor that
// returns a pointer or a slice. Returning a half-decoded record alongside an
// error is the failure mode worth guarding: a caller that logs the error and
// carries on would then act on a struct whose fields are all zero, and a device
// with ID 0 and an empty address looks like a real record.
func TestAccessorsReturnNilOnFailure(t *testing.T) {
	ctx := context.Background()

	// Each accessor reports whether it returned a nil result, so the assertion
	// is on the value as well as on the error.
	accessors := map[string]func(*Client) (bool, error){
		"GetDevice": func(c *Client) (bool, error) {
			got, err := c.GetDevice(ctx, 1)
			return got == nil, err
		},
		"CreateDevice": func(c *Client) (bool, error) {
			got, err := c.CreateDevice(ctx, Device{Name: "core-01"})
			return got == nil, err
		},
		"UpdateDevice": func(c *Client) (bool, error) {
			got, err := c.UpdateDevice(ctx, 1, Device{Name: "core-01"})
			return got == nil, err
		},
		"DeviceStatus": func(c *Client) (bool, error) {
			got, err := c.DeviceStatus(ctx, 1)
			return got == nil, err
		},
		"DeviceMIBs": func(c *Client) (bool, error) {
			got, err := c.DeviceMIBs(ctx, 1)
			return got == nil, err
		},
		"DeviceProfiles": func(c *Client) (bool, error) {
			got, err := c.DeviceProfiles(ctx, 1)
			return got == nil, err
		},
		"CreateProfile": func(c *Client) (bool, error) {
			got, err := c.CreateProfile(ctx, Profile{Name: "core"})
			return got == nil, err
		},
		"UpdateProfile": func(c *Client) (bool, error) {
			got, err := c.UpdateProfile(ctx, 1, Profile{Name: "core"})
			return got == nil, err
		},
		"ProfileMIBs": func(c *Client) (bool, error) {
			got, err := c.ProfileMIBs(ctx, 1)
			return got == nil, err
		},
		"ProfileDevices": func(c *Client) (bool, error) {
			got, err := c.ProfileDevices(ctx, 1)
			return got == nil, err
		},
		"ListDevices": func(c *Client) (bool, error) {
			got, err := c.ListDevices(ctx)
			return got == nil, err
		},
	}

	for name, call := range accessors {
		t.Run(name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "upstream unavailable", http.StatusBadGateway)
			})

			wasNil, err := call(c)
			if err == nil {
				t.Fatalf("%s returned nil error for a 502 response", name)
			}
			if !wasNil {
				t.Errorf("%s returned a non-nil result alongside an error", name)
			}

			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("got %T, want *APIError", err)
			}
			if apiErr.StatusCode != http.StatusBadGateway {
				t.Errorf("StatusCode = %d, want %d", apiErr.StatusCode, http.StatusBadGateway)
			}
		})
	}
}

// TestAccessorsReturnNilOnMalformedResponse covers the other way an accessor
// can fail: a 200 whose body is not what the endpoint promises. This is what a
// misrouted request looks like — the wrong handler answers successfully — and
// it must not be decoded into a half-populated record.
func TestAccessorsReturnNilOnMalformedResponse(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html>not json</html>`))
	})

	device, err := c.GetDevice(context.Background(), 1)
	if err == nil {
		t.Fatal("a non-JSON body was decoded without error")
	}
	if device != nil {
		t.Error("a device was returned alongside a decode error")
	}

	var decodeErr *DecodeError
	if !errors.As(err, &decodeErr) {
		t.Fatalf("got %T, want *DecodeError", err)
	}
	if decodeErr.Path == "" {
		t.Error("the decode error does not say which endpoint produced it")
	}
}

// TestStatusText covers the code-to-text mapping used in every API error
// message. An unmapped code has to yield an empty string rather than a wrong
// one, so the message degrades to the bare number instead of misreporting what
// the server said.
func TestStatusText(t *testing.T) {
	mapped := map[int]string{
		400: "Bad Request",
		401: "Unauthorized",
		403: "Forbidden",
		404: "Not Found",
		409: "Conflict",
		500: "Internal Server Error",
		502: "Bad Gateway",
		503: "Service Unavailable",
	}

	for code, want := range mapped {
		if got := statusText(code); got != want {
			t.Errorf("statusText(%d) = %q, want %q", code, got, want)
		}
	}

	for _, code := range []int{0, 200, 418, 429, 504, -1} {
		if got := statusText(code); got != "" {
			t.Errorf("statusText(%d) = %q, want an empty string for an unmapped code", code, got)
		}
	}
}

// TestAPIErrorMessageUsesStatusText ties the mapping to the message an operator
// actually reads.
func TestAPIErrorMessageUsesStatusText(t *testing.T) {
	err := &APIError{StatusCode: 404, Method: "GET", Path: "/api/devices/9", Body: "device not found"}

	msg := err.Error()
	for _, want := range []string{"GET", "/api/devices/9", "404", "Not Found", "device not found"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Error() = %q, want it to contain %q", msg, want)
		}
	}

	// An unmapped code still produces a usable message.
	unmapped := (&APIError{StatusCode: 418, Method: "GET", Path: "/api/devices"}).Error()
	if !strings.Contains(unmapped, "418") {
		t.Errorf("Error() = %q, want it to report the status code", unmapped)
	}
}

// TestNewRejectsUnparseableURL covers the URL that survives the "://" check but
// that net/url refuses. Accepting it would defer the failure to the first
// request, where the error names a URL the operator never typed.
func TestNewRejectsUnparseableURL(t *testing.T) {
	for _, raw := range []string{
		"http://%zz",       // invalid percent escape
		"http://host:port", // non-numeric port
		"://nohost",        // no scheme before the separator
	} {
		t.Run(raw, func(t *testing.T) {
			c, err := New(raw)
			if err == nil {
				t.Fatalf("New(%q) succeeded, yielding base URL %q", raw, c.BaseURL())
			}

			var configErr *ConfigError
			if !errors.As(err, &configErr) {
				t.Fatalf("got %T, want *ConfigError", err)
			}
			if configErr.Field == "" {
				t.Error("the error does not say which field was wrong")
			}
		})
	}
}

// TestDoRejectsUnencodableBody covers the request that cannot be built at all.
// The check exists so a programming error surfaces as an error rather than as a
// request with an empty body that the server would reject far less clearly.
func TestDoRejectsUnencodableBody(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// A channel has no JSON representation.
	err := c.do(context.Background(), http.MethodPost, "/api/devices", make(chan int), nil)
	if err == nil {
		t.Fatal("a body that cannot be encoded was sent anyway")
	}

	var decodeErr *DecodeError
	if !errors.As(err, &decodeErr) {
		t.Fatalf("got %T, want *DecodeError", err)
	}
}

// TestDoRejectsInvalidMethod covers the failure to construct the request.
func TestDoRejectsInvalidMethod(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	err := c.do(context.Background(), "BAD METHOD", "/api/devices", nil, nil)
	if err == nil {
		t.Fatal("a request with an invalid method was sent anyway")
	}

	var transportErr *TransportError
	if !errors.As(err, &transportErr) {
		t.Fatalf("got %T, want *TransportError", err)
	}
}

// TestImportMIBSendsTags covers the optional tags field of the upload. Tags
// decide which devices a MIB is offered to, so an upload that drops them lands
// the MIB in the store unreachable.
func TestImportMIBSendsTags(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "IF-MIB.txt")
	const contents = "IF-MIB DEFINITIONS ::= BEGIN\nEND\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	var gotTags string
	var gotFilename string
	var gotFileBody string

	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Errorf("parse content type: %v", err)
			return
		}
		reader := multipart.NewReader(r.Body, params["boundary"])
		form, err := reader.ReadForm(1 << 20)
		if err != nil {
			t.Errorf("read multipart form: %v", err)
			return
		}
		if values := form.Value["tags"]; len(values) > 0 {
			gotTags = values[0]
		}
		if files := form.File["file"]; len(files) > 0 {
			gotFilename = files[0].Filename
			f, err := files[0].Open()
			if err != nil {
				t.Errorf("open uploaded file: %v", err)
				return
			}
			defer func() { _ = f.Close() }()
			buf := make([]byte, len(contents))
			n, _ := f.Read(buf)
			gotFileBody = string(buf[:n])
		}
		_, _ = w.Write([]byte(`{"mib":{"id":7,"name":"IF-MIB"},"message":"imported"}`))
	})

	mib, err := c.ImportMIB(context.Background(), path, []string{"cisco", "core"})
	if err != nil {
		t.Fatalf("ImportMIB: %v", err)
	}
	if mib.ID != 7 || mib.Name != "IF-MIB" {
		t.Errorf("got MIB %+v, want the record from the envelope", mib)
	}
	if gotTags != "cisco,core" {
		t.Errorf("tags field = %q, want %q", gotTags, "cisco,core")
	}
	if gotFilename != "IF-MIB.txt" {
		t.Errorf("uploaded filename = %q, want %q", gotFilename, "IF-MIB.txt")
	}
	if gotFileBody != contents {
		t.Errorf("uploaded body = %q, want the file's contents", gotFileBody)
	}
}

// TestImportMIBOmitsTagsWhenNoneGiven checks the other arm: an upload with no
// tags must not send an empty tags field, which the server would read as an
// instruction to clear them.
func TestImportMIBOmitsTagsWhenNoneGiven(t *testing.T) {
	path := filepath.Join(t.TempDir(), "IF-MIB.txt")
	if err := os.WriteFile(path, []byte("IF-MIB DEFINITIONS ::= BEGIN\nEND\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	sawTags := false
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Errorf("parse content type: %v", err)
			return
		}
		form, err := multipart.NewReader(r.Body, params["boundary"]).ReadForm(1 << 20)
		if err != nil {
			t.Errorf("read multipart form: %v", err)
			return
		}
		_, sawTags = form.Value["tags"]
		_, _ = w.Write([]byte(`{"mib":{"id":7,"name":"IF-MIB"}}`))
	})

	if _, err := c.ImportMIB(context.Background(), path, nil); err != nil {
		t.Fatalf("ImportMIB: %v", err)
	}
	if sawTags {
		t.Error("an upload with no tags sent a tags field anyway")
	}
}

// TestImportMIBPropagatesServerError checks that a rejected upload — a MIB that
// does not parse, most often — surfaces the server's explanation rather than a
// bare failure.
func TestImportMIBPropagatesServerError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "BROKEN-MIB.txt")
	if err := os.WriteFile(path, []byte("not a mib"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "parse error at line 1", http.StatusBadRequest)
	})

	mib, err := c.ImportMIB(context.Background(), path, nil)
	if err == nil {
		t.Fatal("a rejected upload reported success")
	}
	if mib != nil {
		t.Error("a MIB record was returned alongside an error")
	}
	if !strings.Contains(err.Error(), "parse error at line 1") {
		t.Errorf("error %q does not carry the server's explanation", err)
	}
	if !strings.Contains(err.Error(), strconv.Itoa(http.StatusBadRequest)) {
		t.Errorf("error %q does not report the status code", err)
	}
}

// TestGetDeviceDecodesTheRecord is the success arm that pairs with the failure
// cases above: the fields the CLI and web UI render must survive the round trip
// rather than being dropped by a JSON tag that no longer matches the server.
func TestGetDeviceDecodesTheRecord(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/devices/42" {
			t.Errorf("path = %q, want /api/devices/42", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
		  "id": 42,
		  "name": "core-01",
		  "address": "198.51.100.101:161",
		  "snmp_version": "2c",
		  "community": "public",
		  "port": 161
		}`))
	})

	device, err := c.GetDevice(context.Background(), 42)
	if err != nil {
		t.Fatalf("GetDevice: %v", err)
	}
	if device.ID != 42 {
		t.Errorf("ID = %d, want 42", device.ID)
	}
	if device.Name != "core-01" {
		t.Errorf("Name = %q, want %q", device.Name, "core-01")
	}
	if device.Address != "198.51.100.101:161" {
		t.Errorf("Address = %q, want %q", device.Address, "198.51.100.101:161")
	}
	if device.SNMPVersion != "2c" {
		t.Errorf("SNMPVersion = %q, want %q", device.SNMPVersion, "2c")
	}
	if device.Port != 161 {
		t.Errorf("Port = %d, want 161", device.Port)
	}
	if device.Community != "public" {
		t.Errorf("Community = %q, want %q", device.Community, "public")
	}
}

// TestImportMIBFileRequestsReplace checks the opt-in reaches the server. Without
// the query the server refuses an already-imported module, so a client that
// drops it turns every refresh into a conflict.
func TestImportMIBFileRequestsReplace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ENTITY-SENSOR-MIB.my")
	if err := os.WriteFile(path, []byte("ENTITY-SENSOR-MIB DEFINITIONS ::= BEGIN\nEND\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	var gotQuery string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(
			`{"mib":{"id":42,"name":"ENTITY-SENSOR-MIB","object_count":31},` +
				`"replaced":true,"message":"Successfully updated MIB ENTITY-SENSOR-MIB with 31 objects"}`))
	})

	result, err := c.ImportMIBFile(context.Background(), path, MIBImportOptions{Replace: true})
	if err != nil {
		t.Fatalf("ImportMIBFile: %v", err)
	}

	if gotQuery != "replace=true" {
		t.Errorf("query = %q, want replace=true", gotQuery)
	}
	if !result.Replaced {
		t.Error("Replaced is false; the caller cannot tell a refresh from an import")
	}
	if result.MIB.ID != 42 || result.MIB.ObjectCount != 31 {
		t.Errorf("MIB = %+v, want the refreshed record", result.MIB)
	}
}

// TestImportMIBFileOmitsReplaceByDefault checks the destructive-adjacent
// behavior stays opt-in: a plain import must not carry the flag.
func TestImportMIBFileOmitsReplaceByDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "IF-MIB.txt")
	if err := os.WriteFile(path, []byte("IF-MIB DEFINITIONS ::= BEGIN\nEND\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	var gotQuery string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"mib":{"id":1,"name":"IF-MIB"},"replaced":false}`))
	})

	result, err := c.ImportMIBFile(context.Background(), path, MIBImportOptions{})
	if err != nil {
		t.Fatalf("ImportMIBFile: %v", err)
	}
	if gotQuery != "" {
		t.Errorf("query = %q, want none", gotQuery)
	}
	if result.Replaced {
		t.Error("Replaced is true for a plain import")
	}
}

// TestImportMIBFileSurfacesConflict checks the server's 409 reaches the caller
// intact. The body is the only place that says how to refresh the module in
// place instead of deleting it, so it must not be swallowed.
func TestImportMIBFileSurfacesConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ENTITY-SENSOR-MIB.my")
	if err := os.WriteFile(path, []byte("ENTITY-SENSOR-MIB DEFINITIONS ::= BEGIN\nEND\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w,
			"MIB ENTITY-SENSOR-MIB is already imported as id 42. "+
				"To refresh its objects in place ... ?replace=true",
			http.StatusConflict)
	})

	_, err := c.ImportMIBFile(context.Background(), path, MIBImportOptions{})

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error is %T, want *APIError", err)
	}
	if apiErr.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want 409", apiErr.StatusCode)
	}
	if apiErr.NotFound() {
		t.Error("a conflict must not be reported as a missing resource")
	}
	if !strings.Contains(apiErr.Error(), "Conflict") {
		t.Errorf("message = %q, want it to name the status", apiErr)
	}
	if !strings.Contains(apiErr.Error(), "replace=true") {
		t.Errorf("message = %q, want the server's guidance kept", apiErr)
	}
}

// TestImportMIBFileMissingFile is the bad-input arm: nothing is sent when the
// file cannot be read.
func TestImportMIBFileMissingFile(t *testing.T) {
	c := newTestClient(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("server must not be called when the file cannot be opened")
	})

	_, err := c.ImportMIBFile(context.Background(),
		filepath.Join(t.TempDir(), "absent.mib"), MIBImportOptions{Replace: true})

	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("error is %T, want *ConfigError", err)
	}
}

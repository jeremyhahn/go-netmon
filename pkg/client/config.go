package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ConfigSchemaVersion is the configuration document version this SDK writes.
// A server that reads a different version rejects the document rather than
// half-understanding it.
const ConfigSchemaVersion = 1

// Config is a go-netmon instance's whole logical configuration: the devices it
// polls, the profiles that bundle MIBs, and every per-assignment rule that says
// how each MIB is polled.
//
// It is keyed by name throughout, because record IDs are local to one instance.
// The MIB catalog itself is not configuration; MIBs is the list of modules the
// configuration depends on, and an export bundle carries their source files
// alongside this document.
type Config struct {
	SchemaVersion int       `json:"schema_version"`
	ExportedAt    time.Time `json:"exported_at"`

	MIBs     []ConfigMIB     `json:"mibs"`
	Profiles []ConfigProfile `json:"profiles"`
	Devices  []ConfigDevice  `json:"devices"`
}

// ConfigMIB is one MIB module the configuration depends on. File and SHA256 are
// set when a bundle carries the module's source.
type ConfigMIB struct {
	Name   string   `json:"name"`
	File   string   `json:"file,omitempty"`
	SHA256 string   `json:"sha256,omitempty"`
	Tags   []string `json:"tags,omitempty"`
}

// ConfigProfile is a named bundle of MIB assignments.
type ConfigProfile struct {
	Name        string             `json:"name"`
	Description string             `json:"description,omitempty"`
	MIBs        []ConfigAssignment `json:"mibs,omitempty"`
}

// ConfigDevice is one monitored device and everything attached to it. An absent
// PollInterval means the device inherits the server's global default.
type ConfigDevice struct {
	Name         string             `json:"name"`
	Address      string             `json:"address"`
	Port         int                `json:"port"`
	SNMPVersion  string             `json:"snmp_version"`
	Community    string             `json:"community,omitempty"`
	PollInterval *int               `json:"poll_interval,omitempty"`
	Profiles     []string           `json:"profiles,omitempty"`
	MIBs         []ConfigAssignment `json:"mibs,omitempty"`
}

// ConfigAssignment is one MIB assignment: the module, and how it is polled.
type ConfigAssignment struct {
	Module string `json:"module"`
	MIBAssignment
}

// ImportReport is what an import did, or would do. The same shape answers a dry
// run and an apply; Applied is the only difference.
type ImportReport struct {
	Applied       bool `json:"applied"`
	SchemaVersion int  `json:"schema_version"`

	// Changed reports whether the configuration would move at all. Importing an
	// instance's own export leaves it false.
	Changed bool `json:"changed"`

	Counts ImportCounts `json:"counts"`

	MIBs     []ConfigMIBChange `json:"mibs"`
	Profiles []ConfigChange    `json:"profiles"`
	Devices  []ConfigChange    `json:"devices"`

	// SafetyExport is where the server saved the configuration it replaced, so
	// a wrong import can be undone by importing that file back. It is set only
	// on an apply.
	SafetyExport string `json:"safety_export,omitempty"`
}

// The actions a planned change can carry.
const (
	ConfigActionCreate = "create"
	ConfigActionUpdate = "update"
	ConfigActionDelete = "delete"
)

// The actions a MIB module can take part in during an import.
const (
	MIBActionImport  = "import"
	MIBActionRefresh = "refresh"
	MIBActionPresent = "present"
	MIBActionMissing = "missing"
)

// ConfigChange is one record an import would create, update or delete. An
// update carries the fields that differ; a create or delete carries Detail.
type ConfigChange struct {
	Action string              `json:"action"`
	Name   string              `json:"name"`
	ID     int64               `json:"id,omitempty"`
	Detail string              `json:"detail,omitempty"`
	Fields []ConfigFieldChange `json:"fields,omitempty"`
}

// ConfigFieldChange is one field moving from one value to another.
type ConfigFieldChange struct {
	Field string `json:"field"`
	From  string `json:"from"`
	To    string `json:"to"`
}

// ConfigMIBChange is what an import does about one MIB module.
type ConfigMIBChange struct {
	Module string `json:"module"`
	Action string `json:"action"`
	File   string `json:"file,omitempty"`
}

// ImportCounts summarizes a report.
type ImportCounts struct {
	DevicesCreated  int `json:"devices_created"`
	DevicesUpdated  int `json:"devices_updated"`
	DevicesDeleted  int `json:"devices_deleted"`
	ProfilesCreated int `json:"profiles_created"`
	ProfilesUpdated int `json:"profiles_updated"`
	ProfilesDeleted int `json:"profiles_deleted"`
	MIBsImported    int `json:"mibs_imported"`
	MIBsRefreshed   int `json:"mibs_refreshed"`
	MIBsPresent     int `json:"mibs_present"`
}

// ConfigExportOptions narrows what an export carries.
type ConfigExportOptions struct {
	// DocumentOnly asks for config.json by itself instead of a bundle. It is
	// the artifact to read and diff; it cannot restore onto an instance that
	// does not already hold every module the configuration references.
	DocumentOnly bool

	// ReferencedMIBsOnly limits the modules carried to the ones some device or
	// profile assigns, rather than the whole imported catalog.
	ReferencedMIBsOnly bool
}

// values renders the options as query parameters.
func (o ConfigExportOptions) values() url.Values {
	values := url.Values{}
	if o.DocumentOnly {
		values.Set("format", "json")
	}
	if o.ReferencedMIBsOnly {
		values.Set("mibs", "referenced")
	}
	return values
}

// ConfigImportOptions controls how a configuration is imported.
type ConfigImportOptions struct {
	// Apply performs the import. Without it the server computes the plan and
	// changes nothing, which is the default because an import replaces the
	// whole configuration and deletes whatever the file does not contain.
	Apply bool
}

// ExportConfigBundle returns the server's configuration export exactly as the
// server produced it.
//
// The bytes are returned rather than a decoded document on purpose. The bundle
// is the artifact: writing it out and reading it back must not lose a field this
// SDK has not been taught about yet, and a build of this SDK older than the
// server it is talking to would do exactly that if it round-tripped the document
// through Go structs.
//
// With ConfigExportOptions.DocumentOnly the bytes are config.json; otherwise
// they are a gzipped tar holding config.json and the MIB source files.
func (c *Client) ExportConfigBundle(ctx context.Context, opts ConfigExportOptions) ([]byte, error) {
	path := withQuery("/api/config/export", opts.values())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, http.NoBody)
	if err != nil {
		return nil, &TransportError{Method: http.MethodGet, URL: c.baseURL + path, Err: err}
	}

	// The request URL is built from the --server address the operator supplied.
	resp, err := c.http.Do(req) // #nosec G704 -- destination is the operator-supplied API server
	if err != nil {
		return nil, &TransportError{Method: http.MethodGet, URL: req.URL.String(), Err: err}
	}
	// Closing a response body only releases the connection; a failure there
	// cannot change what this call already returned.
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &DecodeError{Path: path, Err: err}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, apiError(resp.StatusCode, http.MethodGet, path, body)
	}
	return body, nil
}

// ExportConfig returns the server's configuration as a decoded document.
//
// It always asks for config.json alone: a decoded document cannot hold MIB
// source files, so pretending to have exported them would be worse than not
// offering them. Use ExportConfigBundle to obtain the self-contained archive.
func (c *Client) ExportConfig(ctx context.Context, opts ConfigExportOptions) (*Config, error) {
	opts.DocumentOnly = true

	body, err := c.ExportConfigBundle(ctx, opts)
	if err != nil {
		return nil, err
	}

	var doc Config
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, &DecodeError{Path: "/api/config/export", Err: err}
	}
	return &doc, nil
}

// ImportConfigBundle sends an export bundle or a bare configuration document to
// the server and reports what it did, or would do.
//
// It is a dry run unless ConfigImportOptions.Apply is set. Applying replaces the
// server's whole configuration: anything the file does not contain is deleted.
// The server saves what it replaced first and names the file in the report.
//
// The bytes are passed through untouched, so a document an operator has edited
// by hand reaches the server exactly as written and is judged by the server's
// rules rather than by this SDK's idea of them.
func (c *Client) ImportConfigBundle(
	ctx context.Context, bundle []byte, opts ConfigImportOptions,
) (*ImportReport, error) {
	path := "/api/config/import"
	if opts.Apply {
		path += "?apply=true"
	}

	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(bundle))
	if err != nil {
		return nil, &TransportError{Method: http.MethodPost, URL: c.baseURL + path, Err: err}
	}
	req.Header.Set("Content-Type", contentTypeOf(bundle))
	req.Header.Set("Accept", "application/json")

	var report ImportReport
	if err := c.send(req, http.MethodPost, path, &report); err != nil {
		return nil, configImportError(err)
	}
	return &report, nil
}

// ImportConfig sends a configuration document the caller has built in memory.
//
// Prefer ImportConfigBundle for a file read from disk: it cannot lose a field
// this SDK does not know about, and it is the only form that carries MIB source
// files onto an instance that does not have them.
func (c *Client) ImportConfig(
	ctx context.Context, doc Config, opts ConfigImportOptions,
) (*ImportReport, error) {
	if doc.SchemaVersion == 0 {
		doc.SchemaVersion = ConfigSchemaVersion
	}

	encoded, err := json.Marshal(doc)
	if err != nil {
		return nil, &DecodeError{Path: "/api/config/import", Err: err}
	}
	return c.ImportConfigBundle(ctx, encoded, opts)
}

// contentTypeOf labels an upload by what it actually is. The server sniffs the
// bytes anyway, so this is a courtesy to proxies and logs rather than a claim
// anything depends on.
func contentTypeOf(body []byte) string {
	if len(body) >= 2 && body[0] == 0x1f && body[1] == 0x8b {
		return "application/gzip"
	}
	return "application/json"
}

// ConfigDocumentError reports a configuration the server refused, with the
// detail it supplied.
//
// It is distinct from ConfigError, which reports an unusable client setting:
// this one is about the document, not about this process.
//
// The detail is the point: "400 Bad Request" does not say which field of which
// device is wrong, and "409 Conflict" does not say which MIB modules have to be
// imported first. Both are lists, and both are actionable only as lists.
type ConfigDocumentError struct {
	StatusCode int

	// Kind is the server's machine-readable classification: bundle, schema,
	// validation, missing_mibs, mib_import, safety_export or storage.
	Kind    string
	Message string

	// Problems is set when the document's own fields were rejected.
	Problems []ConfigProblem

	// MissingModules is set when the configuration references MIB modules the
	// server does not hold and the file did not carry.
	MissingModules []string

	// SchemaVersion and SupportedSchemaVersion are set when the document was
	// written by a build the server cannot read.
	SchemaVersion          int
	SupportedSchemaVersion int
}

// ConfigProblem is one field of one record the server rejected.
type ConfigProblem struct {
	Scope   string `json:"scope"`
	Subject string `json:"subject,omitempty"`
	Field   string `json:"field,omitempty"`
	Value   string `json:"value,omitempty"`
	Reason  string `json:"reason"`
}

func (e *ConfigProblem) Error() string {
	subject := e.Scope
	if e.Subject != "" {
		subject += " " + strconv.Quote(e.Subject)
	}
	if e.Field == "" {
		return subject + ": " + e.Reason
	}
	return subject + ": " + e.Field + " " + strconv.Quote(e.Value) + ": " + e.Reason
}

func (e *ConfigDocumentError) Error() string {
	if e.Message != "" {
		return "netmon config: " + e.Message
	}
	return "netmon config: " + strconv.Itoa(e.StatusCode) + " " + statusText(e.StatusCode)
}

// MissingMIBs reports whether the server refused because modules it does not
// have were referenced, which is a prerequisite to satisfy rather than a
// document to correct.
func (e *ConfigDocumentError) MissingMIBs() bool {
	return len(e.MissingModules) > 0
}

// configErrorBody mirrors the server's structured error response.
type configErrorBody struct {
	Kind                   string          `json:"kind"`
	Message                string          `json:"message"`
	Problems               []ConfigProblem `json:"problems"`
	MissingModules         []string        `json:"missing_modules"`
	SchemaVersion          int             `json:"schema_version"`
	SupportedSchemaVersion int             `json:"supported_schema_version"`
}

// configImportError recovers the structured detail a refused configuration
// request carries, leaving anything else as the error it already was.
func configImportError(err error) error {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return err
	}

	var body configErrorBody
	if decodeErr := json.Unmarshal([]byte(apiErr.Body), &body); decodeErr != nil || body.Kind == "" {
		return err
	}

	return &ConfigDocumentError{
		StatusCode:             apiErr.StatusCode,
		Kind:                   body.Kind,
		Message:                strings.TrimSpace(body.Message),
		Problems:               body.Problems,
		MissingModules:         body.MissingModules,
		SchemaVersion:          body.SchemaVersion,
		SupportedSchemaVersion: body.SupportedSchemaVersion,
	}
}

// apiError builds the error a non-2xx response becomes, bounding the body a
// misrouted request can return.
func apiError(status int, method, path string, body []byte) error {
	const maxSnippet = 2048
	if len(body) > maxSnippet {
		body = body[:maxSnippet]
	}
	err := &APIError{
		StatusCode: status,
		Method:     method,
		Path:       path,
		Body:       strings.TrimSpace(string(body)),
	}
	return configImportError(err)
}

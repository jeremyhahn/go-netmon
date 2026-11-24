package backup

import (
	"encoding/json"
)

// ManifestSchema is the manifest version written: 1 has volumes[] and config_files[], 2 adds trees[].
const ManifestSchema = 2

// ManifestFile is the manifest's name inside a backup directory.
const ManifestFile = "manifest.json"

// ChecksumFile is the name of the digest list covering every file in a backup.
const ChecksumFile = "checksums.sha256"

// ConsistencyCold names the only consistency mode this tool offers: the stack is
// stopped, the data is copied at rest, and the stack is started again.
const ConsistencyCold = "cold-offline"

// ToolName identifies this tool in the manifests it writes.
const ToolName = "netmon-backup"

// Manifest describes a backup directory; the JSON field names are the schema.
type Manifest struct {
	SchemaVersion int          `json:"schema_version"`
	BackupName    string       `json:"backup_name"`
	CreatedAt     string       `json:"created_at"`
	CreatedBy     string       `json:"created_by"`
	Tool          string       `json:"tool"`
	Consistency   string       `json:"consistency"`
	Components    []Component  `json:"components"`
	Compose       ComposeInfo  `json:"compose"`
	Images        []ImageInfo  `json:"images"`
	Volumes       []VolumeInfo `json:"volumes"`
	Trees         []TreeInfo   `json:"trees"`
	ConfigFiles   []ConfigInfo `json:"config_files"`
	Logical       LogicalInfo  `json:"logical"`
	Host          HostInfo     `json:"host"`
}

// ComposeInfo records the deployment shape the backup was taken from.
type ComposeInfo struct {
	Project    string   `json:"project"`
	File       string   `json:"file"`
	FileSHA256 string   `json:"file_sha256"`
	Volumes    []string `json:"volumes"`
}

// ImageInfo records exactly what produced the backed-up data.
type ImageInfo struct {
	Service string `json:"service"`
	Image   string `json:"image"`
	ImageID string `json:"image_id"`
}

// VolumeInfo describes one Docker volume carried as an archive member.
type VolumeInfo struct {
	Component     Component `json:"component"`
	ComposeVolume string    `json:"compose_volume"`
	DockerVolume  string    `json:"docker_volume"`
	Member        string    `json:"member"`
	Bytes         int64     `json:"bytes"`
	Entries       int       `json:"entries"`
	SHA256        string    `json:"sha256"`
}

// TreeInfo describes one host directory carried as an archive member.
type TreeInfo struct {
	Component   Component `json:"component"`
	SourcePath  string    `json:"source_path"`
	MountTarget string    `json:"mount_target"`
	Member      string    `json:"member"`
	Bytes       int64     `json:"bytes"`
	Entries     int       `json:"entries"`
	Files       int       `json:"files"`
	SourceBytes int64     `json:"source_bytes"`
	SHA256      string    `json:"sha256"`
}

// ConfigInfo records one bind-mounted repository file.
type ConfigInfo struct {
	Path       string `json:"path"`
	SHA256     string `json:"sha256"`
	Restorable bool   `json:"restorable"`
}

// LogicalInfo records whether a logical export accompanied the physical backup.
type LogicalInfo struct {
	Status string         `json:"status"`
	Counts map[string]int `json:"counts"`
}

// HostInfo records the environment the backup was taken on.
type HostInfo struct {
	DockerVersion  string `json:"docker_version"`
	ComposeVersion string `json:"compose_version"`
}

// Member is the integrity record shared by volume and tree members.
type Member struct {
	Name    string
	SHA256  string
	Bytes   int64
	Entries int
}

// Members lists every archive member the manifest describes.
func (m *Manifest) Members() []Member {
	members := make([]Member, 0, len(m.Volumes)+len(m.Trees))
	for _, v := range m.Volumes {
		members = append(members, Member{Name: v.Member, SHA256: v.SHA256, Bytes: v.Bytes, Entries: v.Entries})
	}
	for _, t := range m.Trees {
		members = append(members, Member{Name: t.Member, SHA256: t.SHA256, Bytes: t.Bytes, Entries: t.Entries})
	}
	return members
}

// Tree returns the tree member for a component.
func (m *Manifest) Tree(c Component) (TreeInfo, bool) {
	for _, t := range m.Trees {
		if t.Component == c {
			return t, true
		}
	}
	return TreeInfo{}, false
}

// Volume returns the volume member for a component.
func (m *Manifest) Volume(c Component) (VolumeInfo, bool) {
	for _, v := range m.Volumes {
		if v.Component == c {
			return v, true
		}
	}
	return VolumeInfo{}, false
}

// Image returns the image a service ran when the backup was taken.
func (m *Manifest) Image(service string) (ImageInfo, bool) {
	for _, i := range m.Images {
		if i.Service == service {
			return i, true
		}
	}
	return ImageInfo{}, false
}

// decodeManifest parses and checks a manifest read from an archive.
func decodeManifest(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, &ManifestError{Path: ManifestFile, Reason: "is not valid JSON", Err: err}
	}
	if m.SchemaVersion == 0 {
		return nil, &ManifestError{Path: ManifestFile, Reason: "has no schema_version"}
	}
	if m.SchemaVersion > ManifestSchema {
		return nil, &SchemaError{Found: m.SchemaVersion, Supported: ManifestSchema}
	}
	for field, present := range map[string]bool{
		"backup_name":     m.BackupName != "",
		"created_at":      m.CreatedAt != "",
		"consistency":     m.Consistency != "",
		"compose.project": m.Compose.Project != "",
		"components":      len(m.Components) > 0,
	} {
		if !present {
			return nil, &ManifestError{Path: ManifestFile, Reason: "is missing required field " + field}
		}
	}
	for _, c := range m.Components {
		if !c.Valid() {
			return nil, &ManifestError{
				Path:   ManifestFile,
				Reason: "names component " + string(c) + " which this tool does not know",
			}
		}
	}
	return &m, nil
}

// encodeManifest renders a manifest the way the archive stores it: indented,
// newline-terminated, so an operator can read it with any tool they like.
func encodeManifest(m *Manifest) ([]byte, error) {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, &ManifestError{Path: ManifestFile, Reason: "cannot be encoded", Err: err}
	}
	return append(data, '\n'), nil
}

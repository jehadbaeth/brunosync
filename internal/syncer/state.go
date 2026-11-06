package syncer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/jehadbaeth/brunosync/internal/gen"
	"github.com/jehadbaeth/brunosync/internal/ocyaml"
)

// StateDir is the folder inside the collection holding brunosync's state. It
// is committed together with the collection and ignored by Bruno.
const StateDir = ".brunosync"

// Manifest records what brunosync generated last time.
type Manifest struct {
	Version   int                     `json:"version"`
	Services  map[string]ServiceState `json:"services"`
	Requests  map[string]RequestState `json:"requests"`
	Variables map[string]string       `json:"variables"` // base values of managed collection variables
	Folders   []string                `json:"folders"`   // folder dirs brunosync created
	// Environments holds the base values of managed environment variables,
	// keyed by environment name, then variable name.
	Environments map[string]map[string]string `json:"environments,omitempty"`
}

// ServiceState describes a synced service.
type ServiceState struct {
	Repo    string `json:"repo"`
	Source  string `json:"source"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version,omitempty"`
	Dir     string `json:"dir"`
}

// RequestState locates a managed request file.
type RequestState struct {
	Service string `json:"service"`
	Path    string `json:"path"` // relative to the collection root, forward slashes
}

func newManifest() *Manifest {
	return &Manifest{
		Version:      1,
		Services:     map[string]ServiceState{},
		Requests:     map[string]RequestState{},
		Variables:    map[string]string{},
		Environments: map[string]map[string]string{},
	}
}

func manifestPath(root string) string { return filepath.Join(root, StateDir, "manifest.json") }

func loadManifest(root string) (*Manifest, error) {
	data, err := os.ReadFile(manifestPath(root))
	if errors.Is(err, os.ErrNotExist) {
		return newManifest(), nil
	}
	if err != nil {
		return nil, err
	}
	m := newManifest()
	if err := json.Unmarshal(data, m); err != nil {
		return nil, err
	}
	if m.Services == nil {
		m.Services = map[string]ServiceState{}
	}
	if m.Requests == nil {
		m.Requests = map[string]RequestState{}
	}
	if m.Variables == nil {
		m.Variables = map[string]string{}
	}
	if m.Environments == nil {
		m.Environments = map[string]map[string]string{}
	}
	return m, nil
}

func (m *Manifest) encode() []byte {
	b, _ := json.MarshalIndent(m, "", "  ")
	return append(b, '\n')
}

// basePath is where the base snapshot for a request key lives.
//
// Snapshots are JSON on purpose: the Bruno CLI runs every .yml request file
// it finds, including ones in ignored folders, so YAML snapshots would be
// executed as requests (seen with bru 4.3.0). Ids that are not file safe ("GET /orders/{id}") get a hash suffix so two ids can never
// share a snapshot.
func basePath(key string) string {
	service, id, _ := strings.Cut(key, "/")
	stem := gen.FileSafe(id)
	if stem != id {
		sum := sha256.Sum256([]byte(id))
		stem += "-" + hex.EncodeToString(sum[:])[:8]
	}
	return StateDir + "/base/" + gen.FileSafe(service) + "/" + stem + ".json"
}

func readYAML(path string) (*ocyaml.Map, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ocyaml.Parse(data)
}

// Package credentials manages private, file-backed overrides of administrator
// supplied credential references. Secrets never form part of its public metadata.
package credentials

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/samekind/soseek/internal/core"
)

const maxKeyBytes = 16 << 10
const maxRecordBytes = 64 << 10

type Metadata struct {
	ID         string   `json:"id"`
	Channels   []string `json:"channels"`
	Providers  []string `json:"providers"`
	Source     string   `json:"source"`
	Configured bool     `json:"configured"`
	Disabled   bool     `json:"disabled"`
	Overridden bool     `json:"overridden"`
}

type reference struct {
	id        string
	source    string
	value     string
	channels  []string
	providers []string
}

type override struct {
	Version  int    `json:"version"`
	Key      string `json:"key,omitempty"`
	Disabled bool   `json:"disabled"`
}

type Manager struct {
	mu     sync.RWMutex
	root   *os.Root
	dir    *os.File
	refs   map[string]*reference
	closed bool
}

// New creates a dedicated 0700 credentials directory beside the database.
// An existing directory must already be private and must not be a symlink.
func New(cfg core.Config) (*Manager, error) {
	if strings.TrimSpace(cfg.Database) == "" || cfg.Database == ":memory:" {
		return nil, credentialError("a filesystem database location is required for the credential vault")
	}
	refs := map[string]*reference{}
	for _, channel := range cfg.Channels {
		ref, err := identify(channel)
		if err != nil {
			if channel.KeyFile == "" && channel.KeyEnv == "" {
				continue
			}
			return nil, err
		}
		if existing := refs[ref.id]; existing != nil {
			ref = existing
		} else {
			refs[ref.id] = ref
		}
		ref.channels = appendUnique(ref.channels, channel.ID)
		ref.providers = appendUnique(ref.providers, channel.Provider)
	}
	for _, ref := range refs {
		sort.Strings(ref.channels)
		sort.Strings(ref.providers)
	}
	parent, err := filepath.Abs(filepath.Dir(cfg.Database))
	if err != nil {
		return nil, credentialError("credential vault location could not be resolved")
	}
	if err = os.MkdirAll(parent, 0700); err != nil {
		return nil, credentialError("credential vault parent directory could not be created")
	}
	parentRoot, err := os.OpenRoot(parent)
	if err != nil {
		return nil, credentialError("credential vault parent directory could not be opened")
	}
	defer parentRoot.Close()
	if err = parentRoot.Mkdir("credentials", 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, credentialError("credential vault directory could not be created")
	}
	before, err := parentRoot.Lstat("credentials")
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 || before.Mode().Perm() != 0700 {
		return nil, credentialError("credential vault must be a private directory with mode 0700 and no symlink")
	}
	root, err := parentRoot.OpenRoot("credentials")
	if err != nil {
		return nil, credentialError("credential vault could not be opened safely")
	}
	dir, err := root.Open(".")
	if err != nil {
		root.Close()
		return nil, credentialError("credential vault could not be opened safely")
	}
	after, err := dir.Stat()
	if err != nil || !after.IsDir() || after.Mode().Perm() != 0700 || !os.SameFile(before, after) {
		dir.Close()
		root.Close()
		return nil, credentialError("credential vault changed while opening")
	}
	m := &Manager{root: root, dir: dir, refs: refs}
	return m, nil
}

// Close releases the pinned directory handles. Calls after Close fail closed.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.closed = true
	err1 := m.dir.Close()
	err2 := m.root.Close()
	if err1 != nil || err2 != nil {
		return credentialError("credential vault could not be closed")
	}
	return nil
}

// ReadKey always checks current disk state; a successful Set, Disable, or Reset
// is therefore visible to the next upstream call without restarting the service.
func (m *Manager) ReadKey(channel core.Channel) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return "", credentialError("credential vault is closed")
	}
	if err := m.checkVault(); err != nil {
		return "", err
	}
	identified, err := identify(channel)
	if err != nil {
		return "", err
	}
	ref := m.refs[identified.id]
	if ref == nil {
		return "", credentialError("credential reference is not part of the deployment")
	}
	record, exists, err := m.readOverride(ref.id)
	if err != nil {
		return "", err
	}
	if exists {
		if record.Disabled {
			return "", credentialError("credential is disabled")
		}
		return record.Key, nil
	}
	return readSource(ref)
}

// List returns only safe metadata. Configured indicates a valid effective key
// exists, independently of the disabled flag; it never includes a masked suffix.
func (m *Manager) List() ([]Metadata, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return nil, credentialError("credential vault is closed")
	}
	if err := m.checkVault(); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(m.refs))
	for id := range m.refs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	list := make([]Metadata, 0, len(ids))
	for _, id := range ids {
		ref := m.refs[id]
		record, exists, err := m.readOverride(id)
		if err != nil {
			return nil, err
		}
		meta := Metadata{ID: id, Channels: append([]string{}, ref.channels...), Providers: append([]string{}, ref.providers...), Source: ref.source, Overridden: exists, Disabled: exists && record.Disabled}
		if exists && record.Key != "" {
			meta.Configured = true
		} else {
			_, err := readSource(ref)
			meta.Configured = err == nil
		}
		list = append(list, meta)
	}
	return list, nil
}

// Set replaces this reference's key for all affected channels and enables it.
func (m *Manager) Set(id, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.known(id); err != nil {
		return err
	}
	normalized, err := validateKey(key)
	if err != nil {
		return err
	}
	return m.writeOverride(id, override{Version: 1, Key: normalized})
}

// Disable persists a deny marker. It never falls back to the deployment key.
func (m *Manager) Disable(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.known(id); err != nil {
		return err
	}
	record, _, err := m.readOverride(id)
	if err != nil {
		return err
	}
	record.Version = 1
	record.Disabled = true
	return m.writeOverride(id, record)
}

// Reset removes the override, restoring the original configured reference.
func (m *Manager) Reset(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.known(id); err != nil {
		return err
	}
	name := id + ".json"
	if _, exists, err := m.overrideInfo(name); err != nil {
		return err
	} else if !exists {
		return nil
	}
	if err := m.root.Remove(name); err != nil {
		return credentialError("credential override could not be removed")
	}
	if err := m.dir.Sync(); err != nil {
		return credentialError("credential reset is effective but could not be durably synchronized")
	}
	return nil
}

func (m *Manager) known(id string) error {
	if m.closed {
		return credentialError("credential vault is closed")
	}
	// Membership is the path allowlist; caller-supplied IDs never become paths.
	if m.refs[id] == nil {
		return core.E("not_found", "credential was not found")
	}
	return m.checkVault()
}

func (m *Manager) checkVault() error {
	info, err := m.dir.Stat()
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return credentialError("credential vault must remain a private directory with mode 0700")
	}
	return nil
}

func identify(channel core.Channel) (*reference, error) {
	if channel.KeyFile != "" && channel.KeyEnv != "" {
		return nil, credentialError("configure exactly one credential file or environment reference")
	}
	ref := &reference{}
	if channel.KeyFile != "" {
		absolute, err := filepath.Abs(channel.KeyFile)
		if err != nil {
			return nil, credentialError("credential reference is invalid")
		}
		ref.source = "file"
		ref.value = filepath.Clean(absolute)
	} else if channel.KeyEnv != "" {
		ref.source = "environment"
		ref.value = channel.KeyEnv
	} else {
		return nil, credentialError("credential reference is not configured")
	}
	// Hash reference identity, never the secret value. Rotating an original key
	// keeps its public ID stable, without disclosing a path or environment name.
	sum := sha256.Sum256([]byte("soseek-credential-reference-v1\x00" + ref.source + "\x00" + ref.value))
	ref.id = "cred_" + hex.EncodeToString(sum[:])
	return ref, nil
}

func (m *Manager) overrideInfo(name string) (os.FileInfo, bool, error) {
	info, err := m.root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, credentialError("credential override could not be inspected")
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return nil, false, credentialError("credential override must be a regular private file with mode 0600")
	}
	return info, true, nil
}

func (m *Manager) readOverride(id string) (override, bool, error) {
	name := id + ".json"
	info, exists, err := m.overrideInfo(name)
	if err != nil || !exists {
		return override{}, exists, err
	}
	f, err := m.root.Open(name)
	if err != nil {
		return override{}, true, credentialError("credential override could not be read")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Mode().Perm() != 0600 || !os.SameFile(info, opened) {
		return override{}, true, credentialError("credential override changed while reading")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxRecordBytes+1))
	if err != nil || len(data) > maxRecordBytes {
		return override{}, true, credentialError("credential override is invalid")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var record override
	if decoder.Decode(&record) != nil {
		return override{}, true, credentialError("credential override is invalid")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || record.Version != 1 {
		return override{}, true, credentialError("credential override is invalid")
	}
	if record.Key != "" {
		normalized, err := validateKey(record.Key)
		if err != nil || normalized != record.Key {
			return override{}, true, credentialError("credential override contains an invalid key")
		}
	} else if !record.Disabled {
		return override{}, true, credentialError("credential override has no key")
	}
	return record, true, nil
}

func (m *Manager) writeOverride(id string, record override) error {
	if _, _, err := m.overrideInfo(id + ".json"); err != nil {
		return err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return credentialError("credential update could not be prepared")
	}
	temp := ".tmp-" + hex.EncodeToString(random[:])
	f, err := m.root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return credentialError("credential update could not be created")
	}
	defer m.root.Remove(temp)
	if err = f.Chmod(0600); err == nil {
		err = json.NewEncoder(f).Encode(record)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return credentialError("credential update could not be written durably")
	}
	if err = m.root.Rename(temp, id+".json"); err != nil {
		return credentialError("credential update could not be installed")
	}
	if err = m.dir.Sync(); err != nil {
		return credentialError("credential update is effective but could not be durably synchronized")
	}
	return nil
}

func readSource(ref *reference) (string, error) {
	if ref.source == "environment" {
		key, err := validateKey(os.Getenv(ref.value))
		if err != nil {
			return "", credentialError("configured credential is missing or invalid")
		}
		return key, nil
	}
	root, err := os.OpenRoot(filepath.Dir(ref.value))
	if err != nil {
		return "", credentialError("configured credential file could not be opened")
	}
	defer root.Close()
	name := filepath.Base(ref.value)
	before, err := root.Lstat(name)
	if err != nil || !before.Mode().IsRegular() {
		return "", credentialError("configured credential file must be a readable regular file")
	}
	f, err := root.Open(name)
	if err != nil {
		return "", credentialError("configured credential file could not be read")
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return "", credentialError("configured credential file changed while reading")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxKeyBytes+3))
	if err != nil || len(data) > maxKeyBytes+2 {
		return "", credentialError("configured credential file is invalid")
	}
	key, err := validateKey(string(data))
	if err != nil {
		return "", credentialError("configured credential is missing or invalid")
	}
	return key, nil
}

func validateKey(key string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" || len(key) >= maxKeyBytes || strings.IndexFunc(key, func(r rune) bool { return unicode.IsControl(r) || r == '\u2028' || r == '\u2029' }) >= 0 {
		return "", core.E("invalid_input", "credential must be a nonempty single line shorter than 16 KiB")
	}
	return key, nil
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func credentialError(message string) *core.Error { return core.E("credential_error", message) }

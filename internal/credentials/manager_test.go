package credentials

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/samekind/soseek/internal/core"
)

func fixture(t *testing.T) (core.Config, string) {
	t.Helper()
	state := t.TempDir()
	keyFile := filepath.Join(t.TempDir(), "private-origin-name.key")
	if err := os.WriteFile(keyFile, []byte("origin-private-key\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOSEEK_PRIVATE_ENV_REFERENCE", "environment-private-key")
	return core.Config{Database: filepath.Join(state, "soseek.db"), Channels: []core.Channel{
		{ID: "exa-official", Provider: "exa", KeyFile: keyFile},
		{ID: "exa-shared", Provider: "exa", KeyFile: keyFile},
		{ID: "tavily-shared", Provider: "tavily", KeyFile: keyFile},
		{ID: "grok-env", Provider: "grok", KeyEnv: "SOSEEK_PRIVATE_ENV_REFERENCE"},
		{ID: "disabled-no-ref", Provider: "grok", Disabled: true},
	}}, keyFile
}

func openFixture(t *testing.T, cfg core.Config) *Manager {
	t.Helper()
	m, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func metadataFor(t *testing.T, m *Manager, source string) Metadata {
	t.Helper()
	list, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, meta := range list {
		if meta.Source == source {
			return meta
		}
	}
	t.Fatalf("source metadata missing: %s", source)
	return Metadata{}
}

func TestReferenceGroupingAndMetadataContainNoSecrets(t *testing.T) {
	cfg, keyFile := fixture(t)
	m := openFixture(t, cfg)
	list, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("expected two references, got %#v", list)
	}
	file := metadataFor(t, m, "file")
	if !reflect.DeepEqual(file.Channels, []string{"exa-official", "exa-shared", "tavily-shared"}) || !reflect.DeepEqual(file.Providers, []string{"exa", "tavily"}) {
		t.Fatalf("reference grouping: %#v", file)
	}
	if !file.Configured || file.Disabled || file.Overridden {
		t.Fatalf("metadata: %#v", file)
	}
	key, err := m.ReadKey(cfg.Channels[0])
	if err != nil || key != "origin-private-key" {
		t.Fatal("configured file reference was not read")
	}
	encoded, _ := json.Marshal(list)
	for _, private := range []string{keyFile, "private-origin-name", "SOSEEK_PRIVATE_ENV_REFERENCE", "origin-private-key", "environment-private-key", "ENV_IGNORED_WHEN_FILE_PRESENT"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("metadata leaked %q", private)
		}
	}
	// Public callers cannot mutate the manager's grouping information.
	file.Channels[0] = "changed"
	fresh := metadataFor(t, m, "file")
	if fresh.Channels[0] != "exa-official" {
		t.Fatal("metadata exposed mutable internal state")
	}
}

func TestAmbiguousFileAndEnvironmentReferencesAreRejected(t *testing.T) {
	cfg, keyFile := fixture(t)
	m := openFixture(t, cfg)
	ambiguous := cfg.Channels[0]
	ambiguous.KeyEnv = "PRIVATE_AMBIGUOUS_ENV"
	if key, err := m.ReadKey(ambiguous); err == nil || key != "" || strings.Contains(err.Error(), keyFile) || strings.Contains(err.Error(), ambiguous.KeyEnv) {
		t.Fatalf("ambiguous reference accepted or leaked: %v", err)
	}
	cfg.Channels[0] = ambiguous
	if _, err := New(cfg); err == nil || strings.Contains(err.Error(), keyFile) || strings.Contains(err.Error(), ambiguous.KeyEnv) {
		t.Fatalf("ambiguous config accepted or leaked: %v", err)
	}
}

func TestSetDisableResetAreImmediateSharedAndDurable(t *testing.T) {
	cfg, keyFile := fixture(t)
	m := openFixture(t, cfg)
	id := metadataFor(t, m, "file").ID
	if err := m.Set(id, "  replacement-private-key\n"); err != nil {
		t.Fatal(err)
	}
	for _, ch := range cfg.Channels[:3] {
		key, err := m.ReadKey(ch)
		if err != nil || key != "replacement-private-key" {
			t.Fatalf("shared replacement unavailable: %v", err)
		}
	}
	original, _ := os.ReadFile(keyFile)
	if string(original) != "origin-private-key\n" {
		t.Fatal("original deployment key was modified")
	}
	meta := metadataFor(t, m, "file")
	if !meta.Overridden || !meta.Configured || meta.Disabled {
		t.Fatalf("rotated metadata: %#v", meta)
	}
	second := openFixture(t, cfg)
	if key, err := second.ReadKey(cfg.Channels[0]); err != nil || key != "replacement-private-key" {
		t.Fatal("replacement did not survive reopening")
	}
	if err := m.Disable(id); err != nil {
		t.Fatal(err)
	}
	for _, reader := range []*Manager{m, second} {
		if key, err := reader.ReadKey(cfg.Channels[0]); err == nil || key != "" {
			t.Fatal("disabled override fell back to deployment key")
		}
	}
	meta = metadataFor(t, m, "file")
	if !meta.Disabled || !meta.Configured || !meta.Overridden {
		t.Fatalf("disabled metadata: %#v", meta)
	}
	if err := m.Set(id, "enabled-new-private-key"); err != nil {
		t.Fatal(err)
	}
	if key, err := second.ReadKey(cfg.Channels[0]); err != nil || key != "enabled-new-private-key" {
		t.Fatal("replacement did not reenable credential")
	}
	if err := m.Reset(id); err != nil {
		t.Fatal(err)
	}
	for _, reader := range []*Manager{m, second} {
		if key, err := reader.ReadKey(cfg.Channels[0]); err != nil || key != "origin-private-key" {
			t.Fatal("reset did not restore deployment key")
		}
	}
	if err := m.Reset(id); err != nil {
		t.Fatal("reset should be idempotent")
	}
	meta = metadataFor(t, m, "file")
	if meta.Disabled || meta.Overridden || !meta.Configured {
		t.Fatalf("reset metadata: %#v", meta)
	}
}

func TestEnvironmentIsReadAtCallTimeAndDisableDoesNotFallback(t *testing.T) {
	cfg, _ := fixture(t)
	m := openFixture(t, cfg)
	id := metadataFor(t, m, "environment").ID
	ch := cfg.Channels[3]
	t.Setenv(ch.KeyEnv, "updated-environment-key")
	if key, err := m.ReadKey(ch); err != nil || key != "updated-environment-key" {
		t.Fatal("environment source was cached")
	}
	if err := m.Disable(id); err != nil {
		t.Fatal(err)
	}
	if key, err := m.ReadKey(ch); err == nil || key != "" {
		t.Fatal("disabled environment credential was used")
	}
	if err := m.Reset(id); err != nil {
		t.Fatal(err)
	}
	if key, err := m.ReadKey(ch); err != nil || key != "updated-environment-key" {
		t.Fatal("environment reference was not restored")
	}
	t.Setenv(ch.KeyEnv, "")
	if metadataFor(t, m, "environment").Configured {
		t.Fatal("empty environment credential shown as configured")
	}
}

func TestVaultAndOverridesUsePrivateModes(t *testing.T) {
	cfg, _ := fixture(t)
	m := openFixture(t, cfg)
	id := metadataFor(t, m, "file").ID
	if err := m.Set(id, "rotation-key"); err != nil {
		t.Fatal(err)
	}
	vault := filepath.Join(filepath.Dir(cfg.Database), "credentials")
	dir, err := os.Stat(vault)
	if err != nil || dir.Mode().Perm() != 0700 {
		t.Fatal("vault mode is not 0700")
	}
	entries, err := os.ReadDir(vault)
	if err != nil || len(entries) != 1 {
		t.Fatal("unexpected temporary files remain")
	}
	file := filepath.Join(vault, id+".json")
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("override mode is not 0600")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err = json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if record["key"] != "rotation-key" || record["disabled"] != false || record["version"] != float64(1) {
		t.Fatal("invalid override record")
	}
}

func TestInvalidKeysAndIDsDoNotChangeStateOrLeak(t *testing.T) {
	cfg, keyFile := fixture(t)
	m := openFixture(t, cfg)
	id := metadataFor(t, m, "file").ID
	for _, key := range []string{"", " \n\t ", "top-secret\nsecond", "top-secret\rsecond", "top-secret\x00second", "top-secret\tsecond", "top-secret\u2028second", strings.Repeat("k", maxKeyBytes)} {
		err := m.Set(id, key)
		if err == nil {
			t.Fatal("invalid key accepted")
		}
		if strings.Contains(err.Error(), "top-secret") {
			t.Fatal("invalid key leaked in error")
		}
	}
	if err := m.Set(id, strings.Repeat("k", maxKeyBytes-1)); err != nil {
		t.Fatal("maximum permitted key rejected")
	}
	if err := m.Reset(id); err != nil {
		t.Fatal(err)
	}
	for _, badID := range []string{"../private-key", "../../escape", keyFile, id + "/../other", ""} {
		for _, mutate := range []func() error{func() error { return m.Set(badID, "new") }, func() error { return m.Disable(badID) }, func() error { return m.Reset(badID) }} {
			if err := mutate(); err == nil || strings.Contains(err.Error(), badID) && badID != "" {
				t.Fatalf("invalid ID was used or exposed: %v", err)
			}
		}
	}
	if _, err := m.ReadKey(core.Channel{KeyFile: filepath.Join(t.TempDir(), "not-in-config")}); err == nil {
		t.Fatal("unregistered file reference accepted")
	}
	if key, err := m.ReadKey(cfg.Channels[0]); err != nil || key != "origin-private-key" {
		t.Fatal("rejected actions changed deployment state")
	}
}

func TestUnsafeVaultAndOverrideFilesFailClosed(t *testing.T) {
	t.Run("vault-symlink", func(t *testing.T) {
		cfg, _ := fixture(t)
		outside := t.TempDir()
		vault := filepath.Join(filepath.Dir(cfg.Database), "credentials")
		if err := os.Symlink(outside, vault); err != nil {
			t.Fatal(err)
		}
		if _, err := New(cfg); err == nil || strings.Contains(err.Error(), outside) {
			t.Fatalf("symlink vault accepted or disclosed: %v", err)
		}
		entries, _ := os.ReadDir(outside)
		if len(entries) != 0 {
			t.Fatal("wrote outside vault")
		}
	})
	t.Run("public-vault", func(t *testing.T) {
		cfg, _ := fixture(t)
		vault := filepath.Join(filepath.Dir(cfg.Database), "credentials")
		if err := os.Mkdir(vault, 0755); err != nil {
			t.Fatal(err)
		}
		if _, err := New(cfg); err == nil {
			t.Fatal("nonprivate vault accepted")
		}
	})
	t.Run("override-symlink", func(t *testing.T) {
		cfg, _ := fixture(t)
		m := openFixture(t, cfg)
		id := metadataFor(t, m, "file").ID
		outside := filepath.Join(t.TempDir(), "outside.json")
		sentinel := []byte(`{"version":1,"key":"outside-secret","disabled":false}`)
		if err := os.WriteFile(outside, sentinel, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(filepath.Dir(cfg.Database), "credentials", id+".json")); err != nil {
			t.Fatal(err)
		}
		if _, err := m.ReadKey(cfg.Channels[0]); err == nil {
			t.Fatal("followed credential symlink")
		}
		if _, err := m.List(); err == nil {
			t.Fatal("listed unsafe override as available")
		}
		for _, err := range []error{m.Set(id, "new-secret"), m.Disable(id), m.Reset(id)} {
			if err == nil || strings.Contains(err.Error(), outside) || strings.Contains(err.Error(), "outside-secret") {
				t.Fatalf("unsafe operation or leak: %v", err)
			}
		}
		after, _ := os.ReadFile(outside)
		if string(after) != string(sentinel) {
			t.Fatal("modified file outside vault")
		}
	})
	t.Run("original-symlink", func(t *testing.T) {
		cfg, keyFile := fixture(t)
		target := filepath.Join(t.TempDir(), "outside-key")
		if err := os.WriteFile(target, []byte("outside-secret"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(keyFile); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, keyFile); err != nil {
			t.Fatal(err)
		}
		m := openFixture(t, cfg)
		if _, err := m.ReadKey(cfg.Channels[0]); err == nil {
			t.Fatal("followed original credential symlink")
		}
		if metadataFor(t, m, "file").Configured {
			t.Fatal("unsafe original marked configured")
		}
	})
	t.Run("nonprivate-override", func(t *testing.T) {
		cfg, _ := fixture(t)
		m := openFixture(t, cfg)
		id := metadataFor(t, m, "file").ID
		if err := m.Set(id, "private-key"); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(filepath.Dir(cfg.Database), "credentials", id+".json"), 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := m.ReadKey(cfg.Channels[0]); err == nil {
			t.Fatal("public override accepted")
		}
	})
	t.Run("permissions-changed-after-open", func(t *testing.T) {
		cfg, _ := fixture(t)
		m := openFixture(t, cfg)
		id := metadataFor(t, m, "file").ID
		if err := os.Chmod(filepath.Join(filepath.Dir(cfg.Database), "credentials"), 0755); err != nil {
			t.Fatal(err)
		}
		if _, err := m.ReadKey(cfg.Channels[0]); err == nil {
			t.Fatal("vault privacy loss did not stop key reads")
		}
		if _, err := m.List(); err == nil {
			t.Fatal("vault privacy loss was hidden from administrator")
		}
		if err := m.Set(id, "must-not-be-written"); err == nil {
			t.Fatal("wrote into nonprivate vault")
		}
	})
}

func TestCorruptOverrideDoesNotFallbackAndCanBeReset(t *testing.T) {
	cfg, _ := fixture(t)
	m := openFixture(t, cfg)
	id := metadataFor(t, m, "file").ID
	filename := filepath.Join(filepath.Dir(cfg.Database), "credentials", id+".json")
	for _, contents := range []string{`{"key":"secret-invalid-version","disabled":false}`, `{"version":1,"key":"secret-key","extra":"private-detail"}`, `{"version":1,"disabled":false}`, `{"version":1,"key":"secret-key","disabled":false} {}`, strings.Repeat("secret", maxRecordBytes)} {
		if err := os.WriteFile(filename, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		key, err := m.ReadKey(cfg.Channels[0])
		if err == nil || key != "" || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), filename) {
			t.Fatalf("corrupt override did not fail privately: %v", err)
		}
	}
	if err := m.Reset(id); err != nil {
		t.Fatal(err)
	}
	if key, err := m.ReadKey(cfg.Channels[0]); err != nil || key != "origin-private-key" {
		t.Fatal("reset did not recover corrupt override")
	}
}

func TestPinnedVaultDoesNotFollowDirectoryReplacement(t *testing.T) {
	cfg, _ := fixture(t)
	m := openFixture(t, cfg)
	id := metadataFor(t, m, "file").ID
	vault := filepath.Join(filepath.Dir(cfg.Database), "credentials")
	moved := vault + "-moved"
	outside := t.TempDir()
	if err := os.Rename(vault, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, vault); err != nil {
		t.Fatal(err)
	}
	if err := m.Set(id, "stays-in-pinned-directory"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(moved, id+".json")); err != nil {
		t.Fatal("pinned directory not used")
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Fatal("credential written through replaced directory symlink")
	}
}

func TestConcurrentReadersAndWritersSeeWholeOverrides(t *testing.T) {
	cfg, _ := fixture(t)
	m := openFixture(t, cfg)
	id := metadataFor(t, m, "file").ID
	var wg sync.WaitGroup
	errCh := make(chan error, 16)
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for iteration := 0; iteration < 30; iteration++ {
				if worker%2 == 0 {
					if err := m.Set(id, fmt.Sprintf("key-%d-%d", worker, iteration)); err != nil {
						errCh <- err
						return
					}
				}
				key, err := m.ReadKey(cfg.Channels[worker%3])
				if err != nil || key != "origin-private-key" && !strings.HasPrefix(key, "key-") {
					errCh <- fmt.Errorf("reader observed invalid whole-key state")
					return
				}
				if _, err := m.List(); err != nil {
					errCh <- err
					return
				}
			}
		}(worker)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
	entries, err := os.ReadDir(filepath.Join(filepath.Dir(cfg.Database), "credentials"))
	if err != nil || len(entries) != 1 {
		t.Fatal("atomic writes left temporary files")
	}
}

func TestClosedManagerFailsClosed(t *testing.T) {
	cfg, _ := fixture(t)
	m := openFixture(t, cfg)
	id := metadataFor(t, m, "file").ID
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ReadKey(cfg.Channels[0]); err == nil {
		t.Fatal("closed manager read a key")
	}
	if _, err := m.List(); err == nil {
		t.Fatal("closed manager listed state")
	}
	for _, err := range []error{m.Set(id, "new"), m.Disable(id), m.Reset(id)} {
		if err == nil {
			t.Fatal("closed manager mutated state")
		}
	}
	if err := m.Close(); err != nil {
		t.Fatal("close is not idempotent")
	}
}

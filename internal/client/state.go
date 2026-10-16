package client

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/jstar0/soseek/internal/core"
)

type Config struct {
	Server    string `json:"server"`
	TokenFile string `json:"token_file"`
}
type Pending struct {
	ID        string        `json:"idempotency_key"`
	Server    string        `json:"server"`
	Identity  string        `json:"identity_fingerprint"`
	CreatedAt time.Time     `json:"created_at"`
	Request   *core.Request `json:"request,omitempty"`
	RunID     string        `json:"run_id,omitempty"`
}

func ConfigDir() (string, error) {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "soseek"), nil
	}
	h, err := os.UserHomeDir()
	return filepath.Join(h, ".config", "soseek"), err
}
func StateDir() (string, error) {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "soseek"), nil
	}
	h, err := os.UserHomeDir()
	return filepath.Join(h, ".local", "state", "soseek"), err
}
func ReadPrivate(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("secret/state path must be a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("%s must be private (chmod 600)", path)
	}
	if info.Size() > 1<<20 {
		return nil, errors.New("secret/state file is too large")
	}
	b, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if len(b) > 1<<20 {
		return nil, errors.New("secret/state file is too large")
	}
	return b, err
}
func WritePrivate(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return WriteSecret(path, append(b, '\n'))
}
func WriteSecret(path string, b []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".soseek-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	return nil
}
func LoadConfig() (Config, error) {
	dir, err := ConfigDir()
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	b, err := ReadPrivate(filepath.Join(dir, "client.json"))
	if err == nil {
		err = json.Unmarshal(b, &cfg)
	} else if os.IsNotExist(err) {
		err = nil
	}
	if err != nil {
		return cfg, err
	}
	if v := os.Getenv("SOSEEK_URL"); v != "" {
		cfg.Server = v
	}
	if v := os.Getenv("SOSEEK_TOKEN_FILE"); v != "" {
		cfg.TokenFile = v
	}
	return cfg, nil
}
func Load() (*Client, error) {
	cfg, err := LoadConfig()
	if err != nil {
		return nil, err
	}
	if cfg.Server == "" || cfg.TokenFile == "" {
		return nil, errors.New("not logged in; run soseek auth login --server URL --token-file FILE")
	}
	b, err := ReadPrivate(cfg.TokenFile)
	if err != nil {
		return nil, err
	}
	return New(cfg.Server, string(b))
}
func Identity(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
func NewPending(c *Client, req core.Request) (Pending, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return Pending{}, err
	}
	p := Pending{ID: hex.EncodeToString(buf), Server: c.BaseURL, Identity: Identity(c.Token), CreatedAt: time.Now().UTC(), Request: &req}
	return p, SavePending(p)
}
func pendingPath(id string) (string, error) {
	if len(id) != 32 {
		return "", errors.New("invalid pending ID")
	}
	if _, err := hex.DecodeString(id); err != nil {
		return "", errors.New("invalid pending ID")
	}
	dir, err := StateDir()
	return filepath.Join(dir, "pending", id+".json"), err
}
func SavePending(p Pending) error {
	path, err := pendingPath(p.ID)
	if err != nil {
		return err
	}
	return WritePrivate(path, p)
}
func LoadPending(id string) (Pending, error) {
	path, err := pendingPath(id)
	if err != nil {
		return Pending{}, err
	}
	b, err := ReadPrivate(path)
	if err != nil {
		return Pending{}, err
	}
	var p Pending
	if err = json.Unmarshal(b, &p); err != nil {
		return p, err
	}
	if p.ID != id {
		return p, errors.New("pending identity mismatch")
	}
	if p.Request != nil && time.Since(p.CreatedAt) > 24*time.Hour {
		_ = os.Remove(path)
		return Pending{}, errors.New("pending request expired after 24 hours and was deleted; it was not resubmitted")
	}
	return p, nil
}
func PendingList() ([]Pending, error) {
	dir, err := StateDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(dir, "pending"))
	if os.IsNotExist(err) {
		return []Pending{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Pending{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		p, err := LoadPending(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			continue
		}
		if p.Request != nil {
			p.Request = nil
			out = append(out, p)
		}
	}
	return out, nil
}
func Acknowledge(p *Pending, runID string) error {
	if runID == "" {
		return errors.New("cannot acknowledge an empty run ID")
	}
	p.Request = nil
	p.RunID = runID
	return SavePending(*p)
}

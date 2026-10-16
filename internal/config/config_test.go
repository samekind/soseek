package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/samekind/soseek/internal/core"
)

func TestRejectUnsafeConfiguration(t *testing.T) {
	c := core.Config{Channels: []core.Channel{{ID: "example", Provider: "exa", BaseURL: "https://api.example.org", KeyEnv: "EXA_KEY", Operations: []string{"search"}, CostBounds: map[string]int64{"search": 1000}}}}
	Defaults(&c)
	if e := Validate(c); e != nil {
		t.Fatal(e)
	}
	for _, base := range []string{"http://api.example.org", "https://user:pass@example.org", "https://example.org?secret=token"} {
		cc := c
		cc.Channels = append([]core.Channel(nil), c.Channels...)
		cc.Channels[0].BaseURL = base
		if Validate(cc) == nil {
			t.Fatal("accepted unsafe upstream URL", base)
		}
	}
	c.Channels[0].CostBounds = nil
	if Validate(c) == nil {
		t.Fatal("accepted operation with no cost bound")
	}
}
func TestUnknownConfigFieldsRejected(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(p, []byte(`{"channels":[],"typo_in_limits":10}`), 0600)
	if _, e := Load(p); e == nil {
		t.Fatal("unknown field ignored")
	}
}

func TestTrustedProxyCIDRs(t *testing.T) {
	c := core.Config{}
	Defaults(&c)
	for _, proxy := range []string{"bad", "0.0.0.0/0", "::/0"} {
		c.TrustedProxies = []string{proxy}
		if Validate(c) == nil {
			t.Fatalf("accepted invalid proxy allowlist %q", proxy)
		}
	}
	c.TrustedProxies = []string{"172.30.4.1/32", "fd00::1/128"}
	if err := Validate(c); err != nil {
		t.Fatal(err)
	}
}

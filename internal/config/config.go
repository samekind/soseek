package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/jstar0/soseek/internal/core"
)

func Load(path string) (core.Config, error) {
	var c core.Config
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if len(b) > 1<<20 {
		return c, fmt.Errorf("configuration is too large")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err = d.Decode(&c); err != nil {
		return c, fmt.Errorf("invalid configuration: %w", err)
	}
	if err = d.Decode(new(any)); err != io.EOF {
		return c, fmt.Errorf("configuration must contain one JSON object")
	}
	Defaults(&c)
	base, _ := filepath.Abs(filepath.Dir(path))
	c.Database = resolve(base, c.Database)
	c.ArtifactDir = resolve(base, c.ArtifactDir)
	for i := range c.Channels {
		if c.Channels[i].KeyFile != "" {
			c.Channels[i].KeyFile = resolve(base, c.Channels[i].KeyFile)
		}
	}
	if err = Validate(c); err != nil {
		return c, err
	}
	sum := sha256.Sum256(b)
	c.Revision = hex.EncodeToString(sum[:8])
	return c, nil
}
func resolve(base, p string) string {
	if strings.HasPrefix(p, "~/") {
		h, _ := os.UserHomeDir()
		return filepath.Join(h, p[2:])
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(base, p)
}
func Defaults(c *core.Config) {
	if c.Listen == "" {
		c.Listen = "127.0.0.1:18570"
	}
	if c.Database == "" {
		c.Database = "data/soseek.db"
	}
	if c.ArtifactDir == "" {
		c.ArtifactDir = "data/artifacts"
	}
	l := &c.Limits
	if l.Workers == 0 {
		l.Workers = 2
	}
	if l.Queue == 0 {
		l.Queue = 20
	}
	if l.DailyMicros == 0 {
		l.DailyMicros = 10_000_000
	}
	if l.DefaultRunMicros == 0 {
		l.DefaultRunMicros = 1_000_000
	}
	if l.MaxRunMicros == 0 {
		l.MaxRunMicros = 2_000_000
	}
	if l.MaxCalls == 0 {
		l.MaxCalls = 12
	}
	if l.MaxRounds == 0 {
		l.MaxRounds = 6
	}
	if l.MaxPages == 0 {
		l.MaxPages = 6
	}
	if l.MaxChars == 0 {
		l.MaxChars = 60000
	}
	if l.MaxDeadline == 0 {
		l.MaxDeadline = 180
	}
	if l.RetentionDays == 0 {
		l.RetentionDays = 7
	}
	if l.CacheSeconds == 0 {
		l.CacheSeconds = 300
	}
	if len(c.SearchOrder) == 0 {
		c.SearchOrder = []string{"exa", "tavily"}
	}
	if len(c.ReadOrder) == 0 {
		c.ReadOrder = []string{"firecrawl", "tavily", "exa"}
	}
	for i := range c.Channels {
		ch := &c.Channels[i]
		if ch.QuotaGroup == "" {
			ch.QuotaGroup = ch.ID
		}
		if ch.RPM == 0 {
			ch.RPM = 10
		}
		if ch.Concurrency == 0 {
			ch.Concurrency = 2
		}
		if ch.TimeoutSeconds == 0 {
			ch.TimeoutSeconds = 45
		}
	}
}
func Validate(c core.Config) error {
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		return fmt.Errorf("listen must be host:port")
	}
	if c.Limits.Workers < 1 || c.Limits.Workers > 32 || c.Limits.Queue < 1 || c.Limits.Queue > 10000 {
		return fmt.Errorf("invalid workers or queue limit")
	}
	if c.Limits.DefaultRunMicros <= 0 || c.Limits.MaxRunMicros < c.Limits.DefaultRunMicros || c.Limits.DailyMicros < c.Limits.DefaultRunMicros {
		return fmt.Errorf("invalid budget limits")
	}
	if c.Limits.MaxCalls < 1 || c.Limits.MaxCalls > 100 || c.Limits.MaxRounds < 1 || c.Limits.MaxRounds > 20 || c.Limits.MaxDeadline < 1 || c.Limits.MaxDeadline > 1800 {
		return fmt.Errorf("invalid execution limits")
	}
	if c.Limits.MaxPages < 1 || c.Limits.MaxPages > 50 || c.Limits.MaxChars < 1000 || c.Limits.MaxChars > 1_000_000 || c.Limits.RetentionDays < 1 || c.Limits.RetentionDays > 90 || c.Limits.CacheSeconds < 1 {
		return fmt.Errorf("invalid retention or content limits")
	}
	seen := map[string]bool{}
	planner := c.PlannerChannel == ""
	for _, ch := range c.Channels {
		if ch.ID == "" || seen[ch.ID] {
			return fmt.Errorf("channel IDs must be unique and nonempty")
		}
		seen[ch.ID] = true
		switch ch.Provider {
		case "exa", "tavily", "firecrawl", "serper", "grok":
		default:
			return fmt.Errorf("channel %s: unsupported provider", ch.ID)
		}
		u, e := url.Parse(ch.BaseURL)
		if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("channel %s: invalid base URL", ch.ID)
		}
		host := u.Hostname()
		ip := net.ParseIP(host)
		local := host == "localhost" || (ip != nil && ip.IsLoopback())
		if u.Scheme != "https" && !(u.Scheme == "http" && local) {
			return fmt.Errorf("channel %s: HTTPS required except loopback test endpoints", ch.ID)
		}
		if !ch.Disabled && ch.KeyFile == "" && ch.KeyEnv == "" {
			return fmt.Errorf("channel %s: secret reference required", ch.ID)
		}
		if ch.RPM < 1 || ch.Concurrency < 1 || ch.TimeoutSeconds < 1 || ch.TimeoutSeconds > 600 {
			return fmt.Errorf("channel %s: invalid limits", ch.ID)
		}
		for _, op := range ch.Operations {
			if ch.CostBounds[op] <= 0 {
				return fmt.Errorf("channel %s: cost bound required for %s", ch.ID, op)
			}
		}
		if ch.ID == c.PlannerChannel {
			if ch.Provider != "grok" || ch.Model == "" || ch.CostBounds["planner"] <= 0 {
				return fmt.Errorf("planner channel requires grok model and planner cost bound")
			}
			planner = true
		}
	}
	if !planner {
		return fmt.Errorf("planner channel not found")
	}
	return nil
}

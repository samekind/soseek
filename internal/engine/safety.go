package engine

import (
	"context"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/jstar0/soseek/internal/core"
)

type resolver func(context.Context, string) ([]net.IPAddr, error)

func defaultResolve(ctx context.Context, host string) ([]net.IPAddr, error) {
	return net.DefaultResolver.LookupIPAddr(ctx, host)
}

// validateURL validates all DNS answers. The remote extraction provider must
// independently enforce redirect/DNS policy; local validation cannot pin its DNS.
func (e *Engine) validateURL(ctx context.Context, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return core.E("unsafe_url", "only absolute HTTP(S) URLs are permitted")
	}
	if u.User != nil {
		return core.E("unsafe_url", "URLs with embedded credentials are not permitted")
	}
	if u.Port() != "" && u.Port() != "80" && u.Port() != "443" {
		return core.E("unsafe_url", "only public HTTP(S) ports 80 and 443 are permitted")
	}
	h := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".internal") {
		return core.E("unsafe_url", "local network names are not permitted")
	}
	ips, err := e.resolve(ctx, h)
	if err != nil || len(ips) == 0 {
		return core.E("unsafe_url", "URL hostname could not be resolved")
	}
	for _, ip := range ips {
		a, ok := netip.AddrFromSlice(ip.IP)
		if !ok || !publicIP(a) {
			return core.E("unsafe_url", "URL resolves to a non-public address")
		}
	}
	return nil
}

var blockedNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"), netip.MustParsePrefix("2002::/16"),
}

func publicIP(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsGlobalUnicast() || a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsUnspecified() {
		return false
	}
	for _, p := range blockedNetworks {
		if p.Contains(a) {
			return false
		}
	}
	return true
}
func (e *Engine) validateTargets(ctx context.Context, call core.Call) error {
	if call.URL != "" {
		if err := e.validateURL(ctx, call.URL); err != nil {
			return err
		}
	}
	var walk func(map[string]any) error
	checkValues := func(value any, keywordsAllowed bool) error {
		var values []string
		switch x := value.(type) {
		case string:
			values = []string{x}
		case []string:
			values = x
		case []any:
			for _, item := range x {
				str, ok := item.(string)
				if !ok {
					return core.E("invalid_parameter", "URL list must contain strings")
				}
				values = append(values, str)
			}
		default:
			return core.E("invalid_parameter", "URL target must be a string or list of strings")
		}
		for _, raw := range values {
			if keywordsAllowed {
				u, err := url.Parse(strings.TrimSpace(raw))
				looksURL := strings.Contains(raw, "://") || strings.HasPrefix(strings.TrimSpace(raw), "//") || (err == nil && u.Scheme != "")
				if !looksURL {
					continue
				}
			}
			if err := e.validateURL(ctx, raw); err != nil {
				return err
			}
		}
		return nil
	}
	walk = func(m map[string]any) error {
		for k, v := range m {
			l := strings.ToLower(k)
			if parts := strings.SplitN(l, ".", 2); len(parts) == 2 {
				switch parts[0] {
				case "exa", "tavily", "firecrawl", "serper", "grok":
					l = parts[1]
				}
			}
			if l == "url" || l == "urls" || l == "start_url" || l == "ids" {
				if err := checkValues(v, false); err != nil {
					return err
				}
			}
			if l == "subpagetarget" {
				if err := checkValues(v, true); err != nil {
					return err
				}
			}
			if (l == "allow_external" || l == "allowexternallinks") && v == true {
				return core.E("unsafe_crawl_scope", "following external crawl links is disabled because newly discovered targets cannot be validated before the upstream fetch")
			}
			if l == "schema" {
				continue
			}
			if m2, ok := v.(map[string]any); ok {
				if err := walk(m2); err != nil {
					return err
				}
			}
			if list, ok := v.([]any); ok {
				for _, item := range list {
					if nested, ok := item.(map[string]any); ok {
						if err := walk(nested); err != nil {
							return err
						}
					}
				}
			}
		}
		return nil
	}
	if err := walk(call.Input); err != nil {
		return err
	}
	return walk(call.Options)
}

type quota struct {
	rpm, concurrency, inflight int
	timestamps                 []time.Time
	coolUntil                  time.Time
	authBlocked                bool
}

func (e *Engine) acquire(ctx context.Context, c core.Channel) (func(), error) {
	g := c.QuotaGroup
	if g == "" {
		g = c.ID
	}
	for {
		e.mu.Lock()
		q := e.groups[g]
		if q == nil {
			q = &quota{rpm: c.RPM, concurrency: c.Concurrency}
			e.groups[g] = q
		}
		now := time.Now()
		if q.authBlocked {
			e.mu.Unlock()
			return nil, core.E("credential_quarantined", "shared credential group is disabled after an authentication failure")
		}
		if q.coolUntil.After(now) {
			seconds := int(time.Until(q.coolUntil).Seconds()) + 1
			e.mu.Unlock()
			return nil, &core.Error{Code: "quota_cooling_down", Message: "shared quota group is cooling down after rate limiting", RetryAfterSeconds: seconds}
		}
		for len(q.timestamps) > 0 && now.Sub(q.timestamps[0]) >= time.Minute {
			q.timestamps = q.timestamps[1:]
		}
		if (q.concurrency <= 0 || q.inflight < q.concurrency) && (q.rpm <= 0 || len(q.timestamps) < q.rpm) {
			q.inflight++
			q.timestamps = append(q.timestamps, now)
			e.mu.Unlock()
			return func() { e.mu.Lock(); q.inflight--; e.mu.Unlock() }, nil
		}
		wait := 50 * time.Millisecond
		if q.rpm > 0 && len(q.timestamps) >= q.rpm {
			wait = time.Until(q.timestamps[0].Add(time.Minute))
		}
		e.mu.Unlock()
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
func (e *Engine) cool(c core.Channel, err *core.Error) {
	if err != nil && err.Code == "upstream_authentication" {
		g := c.QuotaGroup
		if g == "" {
			g = c.ID
		}
		e.mu.Lock()
		if q := e.groups[g]; q != nil {
			q.authBlocked = true
		}
		e.mu.Unlock()
		return
	}
	if err == nil || !(err.Code == "rate_limited" || err.Code == "upstream_rate_limited" || err.Code == "http_429" || err.RetryAfterSeconds > 0) {
		return
	}
	g := c.QuotaGroup
	if g == "" {
		g = c.ID
	}
	wait := err.RetryAfterSeconds
	if wait <= 0 {
		wait = 60
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if q := e.groups[g]; q != nil {
		until := time.Now().Add(time.Duration(wait) * time.Second)
		if until.After(q.coolUntil) {
			q.coolUntil = until
		}
	}
}

func (e *Engine) groupUnavailable(c core.Channel) string {
	g := c.QuotaGroup
	if g == "" {
		g = c.ID
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if q := e.groups[g]; q != nil {
		if q.authBlocked {
			return "credential_quarantined"
		}
		if q.coolUntil.After(time.Now()) {
			return "quota_cooling_down"
		}
	}
	return ""
}

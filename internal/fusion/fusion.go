// Package fusion combines provider evidence without treating repeated queries or
// alternative channels to one provider as independent votes.
package fusion

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"sort"
	"strings"

	"github.com/samekind/soseek/internal/core"
	"golang.org/x/net/publicsuffix"
)

// CanonicalURL deliberately preserves fragments, content query parameters,
// schemes, www and trailing slashes. None are universally interchangeable.
func CanonicalURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	if (u.Scheme == "https" && u.Port() == "443") || (u.Scheme == "http" && u.Port() == "80") {
		h := u.Hostname()
		if strings.Contains(h, ":") {
			h = "[" + h + "]"
		}
		u.Host = h
	}
	q := u.Query()
	for k := range q {
		l := strings.ToLower(k)
		if strings.HasPrefix(l, "utm_") || l == "fbclid" || l == "gclid" || l == "msclkid" {
			q.Del(k)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func Domain(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	h := strings.ToLower(u.Hostname())
	d, err := publicsuffix.EffectiveTLDPlusOne(h)
	if err != nil {
		return h
	}
	return d
}

// Merge assigns no invented rank. Sources with no provider-supplied rank follow
// ranked evidence as supplements. Existing source IDs survive later merges.
func Merge(input []core.Source, limit, perDomain int) []core.Source {
	byURL := map[string]*core.Source{}
	order := []string{}
	for _, in := range input {
		key := CanonicalURL(in.URL)
		if key == "" {
			continue
		}
		s, ok := byURL[key]
		if !ok {
			copy := in
			copy.CanonicalURL = key
			copy.FoundBy = nil
			copy.Occurrences = nil
			byURL[key] = &copy
			s = &copy
			order = append(order, key)
		}
		if s.ID == "" {
			s.ID = in.ID
		}
		if s.Title == "" {
			s.Title = in.Title
		}
		if s.Snippet == "" {
			s.Snippet = in.Snippet
		}
		if len(in.Content) > len(s.Content) {
			s.Content = in.Content
			s.ContentHash = in.ContentHash
			s.FetchedAt = in.FetchedAt
			s.Truncated = in.Truncated
		}
		if s.PublishedAt == "" {
			s.PublishedAt = in.PublishedAt
		}
		for _, p := range in.FoundBy {
			if !contains(s.FoundBy, p) {
				s.FoundBy = append(s.FoundBy, p)
			}
		}
		for _, o := range in.Occurrences {
			if !occurs(s.Occurrences, o) {
				s.Occurrences = append(s.Occurrences, o)
			}
			if !contains(s.FoundBy, o.Provider) {
				s.FoundBy = append(s.FoundBy, o.Provider)
			}
		}
	}
	out := make([]core.Source, 0, len(order))
	for _, key := range order {
		s := byURL[key]
		best := map[string]int{}
		for _, o := range s.Occurrences {
			if o.Rank > 0 && (best[o.Provider] == 0 || o.Rank < best[o.Provider]) {
				best[o.Provider] = o.Rank
			}
		}
		s.Score = 0
		for _, r := range best {
			s.Score += 1 / float64(60+r)
		}
		if s.Content != "" {
			sum := sha256.Sum256([]byte(strings.Join(strings.Fields(s.Content), " ")))
			s.ContentHash = hex.EncodeToString(sum[:])
		}
		sort.Strings(s.FoundBy)
		out = append(out, *s)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		ar, br := bestRank(a), bestRank(b)
		if ar != br {
			return ar < br
		}
		return a.CanonicalURL < b.CanonicalURL
	})
	selected := make([]core.Source, 0, len(out))
	counts := map[string]int{}
	for _, s := range out {
		d := Domain(s.URL)
		if perDomain > 0 && counts[d] >= perDomain {
			continue
		}
		counts[d]++
		selected = append(selected, s)
		if limit > 0 && len(selected) >= limit {
			break
		}
	}
	return selected
}

func bestRank(s core.Source) int {
	n := int(^uint(0) >> 1)
	for _, o := range s.Occurrences {
		if o.Rank > 0 && o.Rank < n {
			n = o.Rank
		}
	}
	return n
}
func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
func occurs(ss []core.Occurrence, o core.Occurrence) bool {
	for _, v := range ss {
		if v == o {
			return true
		}
	}
	return false
}

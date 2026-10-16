package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/jstar0/soseek/internal/core"
)

// Strip complete CSI and OSC sequences, not merely ESC, so terminal source
// text cannot move cursors, set window titles or inject hyperlinks.
var ansi = regexp.MustCompile("\\x1b(?:\\[[0-?]*[ -/]*[@-~]|\\][^\\x07\\x1b]*(?:\\x07|\\x1b\\\\)|[@-_])")

func (a *app) clean(s string) string {
	if a.secret != "" {
		s = strings.ReplaceAll(s, a.secret, "[redacted]")
	}
	s = ansi.ReplaceAllString(s, "")
	return strings.Map(func(r rune) rune {
		if (unicode.IsControl(r) && r != '\n' && r != '\t') || r == '\u202e' || r == '\u202d' || r == '\u202a' || r == '\u202b' || r == '\u202c' || (r >= '\u2066' && r <= '\u2069') {
			return -1
		}
		return r
	}, s)
}
func (a *app) emit(v any) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	// JSON escaping prevents terminal control bytes. Redaction also applies to
	// successful API bodies in case a misconfigured service echoes a token.
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if a.secret != "" {
		escaped, _ := json.Marshal(a.secret)
		b = []byte(strings.ReplaceAll(string(b), string(escaped[1:len(escaped)-1]), "[redacted]"))
	}
	_, err = fmt.Fprintln(a.out, string(b))
	return err
}
func (a *app) print(v any, human string) error {
	if a.json || a.stream {
		return a.emit(v)
	}
	_, err := fmt.Fprintln(a.out, a.clean(human))
	return err
}
func (a *app) diagnostic(message string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	fmt.Fprintln(a.err, a.clean(message))
}
func (a *app) humanResult(obj map[string]any) error {
	if answer, _ := obj["answer"].(string); answer != "" {
		fmt.Fprintln(a.out, a.clean(answer))
	}
	if items, ok := obj["results"].([]any); ok {
		for i, item := range items {
			src, ok := item.(map[string]any)
			if !ok {
				continue
			}
			id, _ := src["source_id"].(string)
			if id == "" {
				id = fmt.Sprintf("S%d", i+1)
			}
			title, _ := src["title"].(string)
			link, _ := src["url"].(string)
			fmt.Fprintf(a.out, "[%s] %s\n%s\n", a.clean(id), a.clean(title), a.clean(link))
			text, _ := src["snippet"].(string)
			if text == "" {
				text, _ = src["content"].(string)
			}
			r := []rune(text)
			if len(r) > 600 {
				text = string(r[:600]) + "… [summary clipped; use --json for full text]"
			}
			if text != "" {
				fmt.Fprintln(a.out, a.clean(text))
			}
			fmt.Fprintln(a.out)
		}
	}
	for _, key := range []string{"warnings", "gaps"} {
		if values, ok := obj[key].([]any); ok {
			for _, v := range values {
				fmt.Fprintln(a.err, a.clean(fmt.Sprint(v)))
			}
		}
	}
	if truncated, _ := obj["truncated"].(bool); truncated {
		fmt.Fprintln(a.err, "Output clipped; full result:", a.clean(fmt.Sprint(obj["full_result_command"])))
	}
	if usage, ok := obj["usage"].(map[string]any); ok {
		micros := func(key string) int64 { v, _ := usage[key].(float64); return int64(v) }
		fmt.Fprintf(a.err, "Usage: upstream-reported USD %s; estimated USD %s; reserved USD %s.\n", core.USD(micros("reported_micros")), core.USD(micros("estimated_micros")), core.USD(micros("reserved_micros")))
		if n := micros("unsettled_attempts"); n > 0 {
			fmt.Fprintf(a.err, "Unsettled attempts: %d. Their final charges are unknown; reported totals are not the final bill.\n", n)
		}
		if credits, ok := usage["credits"].(float64); ok && credits != 0 {
			fmt.Fprintf(a.err, "Provider credits: %g (not added to USD).\n", credits)
		}
	}
	fmt.Fprintln(a.err, "Run:", a.clean(fmt.Sprint(obj["run_id"])), "Status:", a.clean(fmt.Sprint(obj["status"])))
	return nil
}

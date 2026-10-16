package engine

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/samekind/soseek/internal/core"
)

type availabilityProvider struct {
	*fakeProvider
	mu       sync.RWMutex
	disabled map[string]bool
}

func (p *availabilityProvider) CredentialAvailable(c core.Channel) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return !p.disabled[c.ID]
}
func (p *availabilityProvider) setDisabled(id string, disabled bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.disabled[id] = disabled
}

func TestCredentialDisableRoutesOnlyToAllowedAlternativeAndRestoresLive(t *testing.T) {
	e, _, up := fixture()
	addExaProxy(e)
	availability := &availabilityProvider{fakeProvider: up, disabled: map[string]bool{"exa": true}}
	e.up = availability
	p := core.Principal{ID: "p"}
	plan, err := e.Plan(core.Request{Query: "q", Provider: "exa"}, p)
	if err != nil || len(plan.Steps) != 1 || plan.Steps[0].Channel != "exa-proxy" {
		t.Fatal("disabled credential did not select same-provider alternative", plan, err)
	}
	if !strings.Contains(strings.Join(plan.Warnings, " "), "exa excluded: referenced credential is unavailable or disabled") {
		t.Fatal("credential exclusion reason missing", plan.Warnings)
	}
	called := []string{}
	up.call = func(_ context.Context, c core.Channel, _ core.Call) (core.Output, error) {
		called = append(called, c.ID)
		return core.Output{Sources: []core.Source{{URL: "https://example.org/evidence"}}, Usage: core.Usage{CostKnown: true}}, nil
	}
	r := run(t, e, core.Request{Query: "q", Provider: "exa"}, "credential-fallback", "p")
	if r.Status != "succeeded" || len(called) != 1 || called[0] != "exa-proxy" {
		t.Fatal("execution attempted a disabled credential", called, r)
	}
	_, err = e.Plan(core.Request{Query: "q", Provider: "exa", Channel: "exa"}, p)
	if err == nil || !strings.Contains(err.Error(), "referenced credential is unavailable or disabled") {
		t.Fatal("pinned disabled credential accepted", err)
	}
	var capabilities struct {
		Providers map[string]any `json:"providers"`
		Channels  []struct {
			ID        string `json:"id"`
			Available bool   `json:"available"`
			Reason    string `json:"unavailable_reason"`
		} `json:"channels"`
	}
	data, _ := json.Marshal(e.Capabilities(p))
	if err = json.Unmarshal(data, &capabilities); err != nil {
		t.Fatal(err)
	}
	if _, exists := capabilities.Providers["exa"]; !exists {
		t.Fatal("credential disable removed provider implementation")
	}
	foundDisabled, foundAlternative := false, false
	for _, c := range capabilities.Channels {
		if c.ID == "exa" {
			foundDisabled = !c.Available && c.Reason == "referenced credential is unavailable or disabled"
		}
		if c.ID == "exa-proxy" {
			foundAlternative = c.Available
		}
	}
	if !foundDisabled || !foundAlternative {
		t.Fatal("capabilities misreported channel availability", string(data))
	}
	availability.setDisabled("exa-proxy", true)
	if has(e.availableSearch(p), "exa") {
		t.Fatal("agent can still select a provider whose credentials are all disabled")
	}
	_, err = e.Plan(core.Request{Query: "q", Provider: "exa"}, p)
	if err == nil {
		t.Fatal("explicit provider silently crossed to another provider")
	}
	availability.setDisabled("exa", false)
	plan, err = e.Plan(core.Request{Query: "q", Provider: "exa"}, p)
	if err != nil || plan.Steps[0].Channel != "exa" || !has(e.availableSearch(p), "exa") {
		t.Fatal("credential recovery required restart", plan, err)
	}
}

func TestPlannerCredentialDisableAndRestoreChangesDiscoveryWithoutCalls(t *testing.T) {
	e, _, up := fixture()
	availability := &availabilityProvider{fakeProvider: up, disabled: map[string]bool{"grok": true}}
	e.up = availability
	p := core.Principal{ID: "p"}
	_, err := e.Plan(core.Request{Operation: "research", Query: "q"}, p)
	if err == nil || !strings.Contains(err.Error(), "referenced credential is unavailable or disabled") {
		t.Fatal("disabled planner credential was accepted", err)
	}
	if has(e.availableSearch(p), "grok") {
		t.Fatal("agent search provider list includes disabled Grok credential")
	}
	capability := e.Capabilities(p).(map[string]any)
	profiles := capability["profiles"].(map[string]any)
	if profiles["deep"].(map[string]any)["planner_available"] != false {
		t.Fatal("planner discovery ignores credential state")
	}
	availability.setDisabled("grok", false)
	if _, err = e.Plan(core.Request{Operation: "research", Query: "q"}, p); err != nil {
		t.Fatal("planner credential restore did not take effect", err)
	}
	capability = e.Capabilities(p).(map[string]any)
	if capability["profiles"].(map[string]any)["deep"].(map[string]any)["planner_available"] != true {
		t.Fatal("restored planner remains unavailable")
	}
}

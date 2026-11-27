package egress

import (
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func valuePointer[T any](value T) *T { return &value }

func TestFullLayerPrecedence(t *testing.T) {
	allow := api.Rule{Allow: true, Target: "api.example.com"}
	deny := api.Rule{Target: "api.example.com"}
	cases := []struct {
		name   string
		layers []api.Layer
		want   bool
		locked bool
	}{
		{"session over all", []api.Layer{{Kind: api.LayerAll, Rules: []api.Rule{deny}}, {Kind: api.LayerSession, Rules: []api.Rule{allow}}}, true, false},
		{"profile after repository", []api.Layer{{Kind: api.LayerRepo, Name: "forge.example/team/app", Rules: []api.Rule{allow}}, {Kind: api.LayerProfile, Name: "review", Rules: []api.Rule{deny}}}, false, false},
		{"exact after organisation", []api.Layer{{Kind: api.LayerRepo, Name: "forge.example/team/*", Rules: []api.Rule{deny}}, {Kind: api.LayerRepo, Name: "forge.example/team/app", Rules: []api.Rule{allow}}}, true, false},
		{"reverse repository order", []api.Layer{{Kind: api.LayerRepo, Name: "forge.example/team/app", Rules: []api.Rule{allow}}, {Kind: api.LayerRepo, Name: "forge.example/team/*", Rules: []api.Rule{deny}}}, false, false},
		{"deny within one layer", []api.Layer{{Kind: api.LayerSession, Rules: []api.Rule{allow, deny}}}, false, false},
		{"all lock over session", []api.Layer{{Kind: api.LayerAll, Lock: &api.Lock{Deny: []string{"api.example.com"}}}, {Kind: api.LayerSession, Rules: []api.Rule{allow}}}, false, true},
		{"late host lock", []api.Layer{{Kind: api.LayerAll, Rules: []api.Rule{allow}}, {Kind: api.LayerHost, Name: "box", Lock: &api.Lock{Deny: []string{"api.example.com"}}}}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			policy, clean, err := MergeLayers(tc.layers)
			if tc.locked {
				var pe *proto.Error
				if !errors.As(err, &pe) || pe.Code != "policy_locked" {
					t.Fatalf("want policy_locked, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			engine, err := NewEngine("", policy)
			if err != nil {
				t.Fatal(err)
			}
			decision := engine.Decide(Request{Host: "api.example.com", Port: 443})
			if decision.Allow != tc.want {
				t.Fatalf("decision=%+v want allow=%v", decision, tc.want)
			}
			again, _, err := MergeLayers(clean)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(policy, again) {
				t.Fatal("clean layers do not round trip")
			}
		})
	}
}

func TestLayerValuesListsLocksAndRepositoryNarrowing(t *testing.T) {
	layers := []api.Layer{
		{Kind: api.LayerAll, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Secrets: []string{"FIRST", "SECOND"}, Env: map[string]string{"MODE": "all", "KEEP": "yes"}, Wall: valuePointer("microvm")}}},
		{Kind: api.LayerRepo, Name: "forge.example/team/app", LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Secrets: []string{"!FIRST", "THIRD"}, Env: map[string]string{"MODE": "repo"}, Wall: valuePointer("process")}}},
	}
	policy, _, err := MergeLayers(layers)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(policy.Sandbox.Secrets, []string{"SECOND", "THIRD"}) {
		t.Fatalf("secrets=%v", policy.Sandbox.Secrets)
	}
	if policy.Sandbox.Env["MODE"] != "repo" || policy.Sandbox.Env["KEEP"] != "yes" {
		t.Fatalf("env=%v", policy.Sandbox.Env)
	}
	if *policy.Sandbox.Wall != "microvm" {
		t.Fatalf("wall=%s", *policy.Sandbox.Wall)
	}
	layers[0].Lock = &api.Lock{LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Paste: valuePointer("media")}}}
	layers = append(layers, api.Layer{Kind: api.LayerSession, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Paste: valuePointer("any")}}})
	if _, _, err := MergeLayers(layers); err == nil {
		t.Fatal("session changed locked paste")
	}
	for _, values := range []api.LayerValues{
		{Sandbox: api.SandboxValues{Preset: valuePointer("open")}},
		{Network: api.NetworkValues{Unknown: valuePointer("allow")}},
		{Sandbox: api.SandboxValues{Secrets: []string{"TOKEN"}}},
		{Network: api.NetworkValues{Allow: []string{"api.example.com"}}},
	} {
		if _, _, err := MergeLayers([]api.Layer{{Kind: api.LayerRepo, Name: "forge.example/team/app", Source: api.SourceRepositoryFile, LayerValues: values}}); err == nil {
			t.Fatalf("repository widened with %+v", values)
		}
	}
	if _, _, err := MergeLayers([]api.Layer{{Kind: api.LayerRepo, Name: "forge.example/team/app", Source: api.SourceRepositoryFile, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Preset: valuePointer("locked"), Wall: valuePointer("microvm")}, Network: api.NetworkValues{Deny: []string{"api.example.com"}, Unknown: valuePointer("deny")}}}}); err != nil {
		t.Fatal(err)
	}
}

func TestMalformedExplicitLayerOrderFailsClosed(t *testing.T) {
	r := api.Rule{Layer: api.LayerRepo, Name: "forge.example/team/app", Allow: true, Target: "api.example.com"}
	for _, order := range [][]api.LayerRef{
		{{Kind: api.LayerRepo, Name: "other"}},
		{{Kind: api.LayerRepo, Name: r.Name}, {Kind: api.LayerRepo, Name: r.Name}},
		{{Kind: api.LayerSession}, {Kind: api.LayerRepo, Name: r.Name}},
		{{Kind: "future"}, {Kind: api.LayerRepo, Name: r.Name}},
	} {
		if _, err := NewEngine("", api.Policy{Preset: api.PresetLocked, Order: order, Rules: []api.Rule{r}}); err == nil {
			t.Fatalf("accepted malformed order %v", order)
		}
	}
	if _, _, err := MergeLayers([]api.Layer{{Kind: api.LayerRepo, Name: r.Name}, {Kind: api.LayerRepo, Name: r.Name}}); err == nil {
		t.Fatal("accepted duplicate layer references")
	}
}

func TestOrderedLayerOnceAndExpiryRemainCanonical(t *testing.T) {
	now := time.Now()
	policy, _, err := MergeLayers([]api.Layer{{Kind: api.LayerRepo, Name: "first"}, {Kind: api.LayerRepo, Name: "second", Rules: []api.Rule{{Allow: true, Target: "one.example", Once: true, GrantID: "instance"}, {Allow: true, Target: "time.example", Until: now.Add(time.Minute)}}}})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine("", policy)
	if err != nil {
		t.Fatal(err)
	}
	var allowed atomic.Int32
	start := make(chan struct{})
	var wait sync.WaitGroup
	for range 32 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			if engine.Decide(Request{Host: "one.example", Port: 443}).Allow {
				allowed.Add(1)
			}
		}()
	}
	close(start)
	wait.Wait()
	if allowed.Load() != 1 {
		t.Fatalf("once allowed %d concurrent flows", allowed.Load())
	}
	if !engine.Decide(Request{Host: "time.example", Port: 443, Now: now}).Allow || engine.Decide(Request{Host: "time.example", Port: 443, Now: now.Add(time.Minute)}).Allow {
		t.Fatal("expiry boundary changed")
	}
}

func TestRepositoryFileHasDistinctTypedIdentity(t *testing.T) {
	name := "forge.example/team/app"
	policy, _, err := MergeLayers([]api.Layer{
		{Kind: api.LayerRepo, Name: "forge.example/team/*", Rules: []api.Rule{{Target: "api.example.com"}}},
		{Kind: api.LayerRepo, Name: name, Rules: []api.Rule{{Allow: true, Target: "api.example.com"}}},
		{Kind: api.LayerRepo, Name: name, Source: api.SourceRepositoryFile, Rules: []api.Rule{{Target: "api.example.com"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(policy.Order) != 3 || policy.Order[1] == policy.Order[2] {
		t.Fatalf("colliding refs: %v", policy.Order)
	}
	engine, err := NewEngine("", policy)
	if err != nil {
		t.Fatal(err)
	}
	decision := engine.Decide(Request{Host: "api.example.com", Port: 443})
	if decision.Allow || decision.Match == nil || decision.Match.Source != api.SourceRepositoryFile {
		t.Fatalf("wrong deciding source: %+v", decision)
	}
	policy.Order = nil
	if _, err := NewEngine("", policy); err == nil {
		t.Fatal("new shape without explicit order was treated as legacy")
	}
	if _, err := NewEngine("", api.Policy{Preset: api.PresetLocked, Rules: []api.Rule{{Layer: api.LayerSession, Allow: true, Target: "api.example.com"}}}); err != nil {
		t.Fatalf("genuine legacy policy rejected: %v", err)
	}
}

func TestLegacyNamedLayersLeasesAndNonNetModesStayValid(t *testing.T) {
	until := time.Now().Add(time.Hour)
	layers := []api.Layer{
		{Kind: api.LayerAll, Rules: []api.Rule{{Allow: true, Target: "api.example.com", Until: until}}},
		{Kind: api.LayerRepo, Name: "legacy-one", Rules: []api.Rule{{Allow: true, Target: "other.example.com"}, {Door: "mount", Allow: true, Target: "/data", Mode: "ro"}}},
		{Kind: api.LayerRepo, Name: "legacy-two", Rules: []api.Rule{{Target: "other.example.com"}, {Door: "mount", Allow: true, Target: "/other", Mode: "rw"}}},
		{Kind: api.LayerSession, Rules: []api.Rule{{Door: "mount", Allow: true, Target: "/data", Mode: "ro"}}},
	}
	policy, _, err := MergeLayers(layers)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.Rules[0].Until.Equal(until) {
		t.Fatal("legacy future lease was changed")
	}
	legacy := api.Policy{Preset: api.PresetLocked, Rules: []api.Rule{
		{Layer: api.LayerRepo, Name: "legacy-one", Allow: true, Target: "other.example.com"},
		{Layer: api.LayerRepo, Name: "legacy-two", Target: "other.example.com"},
	}}
	engine, err := NewEngine("", legacy)
	if err != nil {
		t.Fatal(err)
	}
	if engine.Decide(Request{Host: "other.example.com", Port: 443}).Allow {
		t.Fatal("legacy names changed old kind-only deny precedence")
	}
}

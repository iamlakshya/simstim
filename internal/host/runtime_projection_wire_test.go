//go:build !windows

package host

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestRuntimeOriginRawCarrierCannotEnterUnsolicitedNewOrResumePhasesV1(t *testing.T) {
	setupIsolatedPrepareHost(t)
	full, _ := runtimeOriginFullFixtureV1(t)
	origin := runtimeOriginRawFixtureV1(t, full)
	b := &bridge{ctx: t.Context(), hostID: strings.Repeat("a", 32)}
	for _, level := range []string{"none", "process", "gvisor"} {
		a := NewArgs{ID: "abcd2345", Wall: level, RuntimeOrigin: origin}
		raw, err := json.Marshal(a)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := b.newRequestContext(proto.Request{ID: 1, Op: "new", Args: raw}, a); err == nil {
			t.Fatal("non-VM New accepted origin", level)
		}
		if _, err := NewContext(t.Context(), a); err == nil {
			t.Fatal("SDK metadata manufactured bridge origin", level)
		}
		duplicate := append(bytes.TrimSuffix(raw, []byte("}")), []byte(`,"runtime_origin_v1":`)...)
		duplicate = append(duplicate, origin...)
		duplicate = append(duplicate, '}')
		if _, err := decodeNewArgs(duplicate); err == nil {
			t.Fatal("outer New duplicate origin was accepted")
		}
	}
	first, err := json.Marshal(map[string]any{"v": 1, "prepare_vm": true, "start": map[string]any{"id": "abcd2345", "runtime_origin_v1": origin}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.prepareResumeV1(&bridgeResumeServiceV1{}, first); err == nil {
		t.Fatal("phase1 Resume accepted phase2-only origin")
	}
	final, err := json.Marshal(map[string]any{"id": "abcd2345", "runtime_origin_v1": origin})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.resumePreparedV1(proto.Request{ID: 2, Op: "resume", Args: final}, api.ResumeArgs{ID: "abcd2345"}); err == nil {
		t.Fatal("final Resume accepted phase2-only origin")
	}
}

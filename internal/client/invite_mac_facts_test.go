package client

import (
	"context"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

func TestJoinedMacFactsAreStrictBoundedAdvisoryData(t *testing.T) {
	good := `{"v":1,"kind":"mac","macos":"15.6","chip":"Apple Silicon","on_battery_known":false,"battery_known":false,"ac_sleep_known":false,"mdm_known":false,"sshd_managed_known":false,"thermal_known":false,"start_mode":"daemon","launch_known":false}`
	if err := validateJoinedMacFactsV1([]byte(good)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`[]`, `null`, good + good, strings.Replace(good, `"v":1`, `"v":2`, 1), strings.Replace(good, `"v":1`, `"v":1,"v":1`, 1), strings.Replace(good, `"kind":"mac"`, `"kind":"mac","command":"whoami"`, 1), strings.Replace(good, `"chip":"Apple Silicon"`, `"chip":"bad\nvalue"`, 1), strings.Replace(good, `"chip":"Apple Silicon"`, `"chip":"`+strings.Repeat("a", 257)+`"`, 1), strings.Repeat(" ", joinedMacFactsLimitV1+1)} {
		if err := validateJoinedMacFactsV1([]byte(bad)); err == nil {
			t.Fatal("unbounded or malformed facts were accepted")
		}
	}
	if _, err := ReadJoinedMacFactsV1(context.Background(), nil); err == nil {
		t.Fatal("data without an actual original Conn manufactured a facts route")
	}
	if _, err := ReadJoinedMacFactsV1(context.Background(), &Conn{}); err == nil {
		t.Fatal("a fabricated Conn manufactured a facts route")
	}
}
func TestJoinedMacFactsBufferCannotGrowPastItsBound(t *testing.T) {
	buffer := &joinedFactsBufferV1{limit: 32}
	if _, err := buffer.Write(make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	if _, err := buffer.Write([]byte{1}); err == nil || !buffer.exceeded || buffer.Len() != 32 {
		t.Fatal("facts output grew past its actual bounded writer")
	}
}

func TestJoinedLinuxFactsSkipMacRouteAndKeepOriginalSource(t *testing.T) {
	host := savedJoinedFixture(t)
	transport := newPrepareBridgeTransport(t, strings.Repeat("a", 32), nil, func(proto.Request, *proto.Writer) (any, *proto.Error, bool) {
		return nil, &proto.Error{Code: "unexpected", Message: "unexpected Linux facts fixture operation"}, false
	})
	connection, err := dial(t.Context(), host, route{t: transport})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	body, err := ReadJoinedMacFactsV1(t.Context(), connection)
	if err != nil || len(body) != 0 {
		t.Fatalf("ordinary Linux join became a Mac claim: %q %v", body, err)
	}
	if err = SaveHosts(t.Context(), []Host{host}); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadJoinedMacFactsV1(t.Context(), connection); err == nil {
		t.Fatal("a stale original connection adopted a fresh same-name Source")
	}
}

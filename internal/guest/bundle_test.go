package guest

import (
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/gitpolicy"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestBundleHeadSnapshotRejectsOutsideAndDuplicateRefs(t *testing.T) {
	prefix := "refs/heads/" + brand.Name + "/primary2/"
	oid := strings.Repeat("a", 40)
	good := oid + " " + prefix + "topic\n"
	heads, err := parseBundleHeads(good, prefix)
	if err != nil || len(heads) != 1 || heads[0].OID != oid {
		t.Fatalf("positive snapshot control: %+v %v", heads, err)
	}
	for _, data := range []string{good + good, oid + " refs/tags/bait\n", oid + " " + prefix + "\n", "-bad " + prefix + "topic\n", oid + " " + prefix + "topic\r\n"} {
		if _, err := parseBundleHeads(data, prefix); err == nil {
			t.Fatal("unsafe snapshot accepted", data)
		}
	}
}

func TestBundleCommandsUseCanonicalPolicyAndNoSecretEnvironment(t *testing.T) {
	command := bundleCommand(strings.Repeat("a", 26), "/work/repo", "bundle", "create", "-", "refs/heads/topic")
	policy := gitpolicy.Args()
	if command.Argv[0] != "/usr/bin/git" || !slices.Equal(command.Argv[1:1+len(policy)], policy) || command.TimeoutMS != 0 {
		t.Fatal("bundle forked the Git policy or introduced exec's whole-operation deadline")
	}
	for _, item := range bundleEnvironment() {
		key, _, _ := strings.Cut(item, "=")
		if key == "SSH_AUTH_SOCK" || strings.Contains(key, "TOKEN") || strings.Contains(key, "SECRET") || strings.HasSuffix(key, "API_KEY") {
			t.Fatal("bundle received a session or login credential")
		}
	}
}

func TestBundlePrepareRejectsInvalidMetadataBeforeAnyRunner(t *testing.T) {
	g := &Init{}
	for _, a := range []proto.CtlBundleArgs{
		{ID: "invalid!", Primary: "primary2", Cwd: "/work/repo"},
		{ID: strings.Repeat("a", 26), Primary: "primary2", Cwd: "/home/agent"},
		{ID: strings.Repeat("a", 26), Primary: "primary2", Cwd: "/work/../etc"},
		{ID: strings.Repeat("a", 26), Primary: "primary2", Cwd: "/work/repo", SeedHeads: []string{"guest claim"}},
		{ID: strings.Repeat("a", 26), Primary: "primary2", Cwd: "/work/repo", HeadCount: 1, HeadsDigest: "forged"},
	} {
		if _, err := g.prepareBundle(a); err == nil {
			t.Fatal("invalid preparation reached the canonical runner")
		}
	}
}

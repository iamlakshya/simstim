package host

import (
	"strings"
	"testing"
)

func activationPropertyFixture() string {
	return "InvocationID=0123456789abcdef0123456789abcdef\nMainPID=4321\nControlGroup=/user.slice/session.service\nType=exec\n"
}

func TestUnitActivationPropertiesDecodeAndCompare(t *testing.T) {
	got, err := decodeUnitActivationProperties([]byte(activationPropertyFixture()))
	if err != nil {
		t.Fatal(err)
	}
	if got.MainPID != 4321 || got.Type != "exec" || got.ControlGroup != "/user.slice/session.service" || got.InvocationID != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("wrong manager observation: %#v", got)
	}
	if !sameUnitActivationProperties(got, got) {
		t.Fatal("the same valid observation did not match")
	}
	changed := []unitActivationProperties{got, got, got, got}
	changed[0].InvocationID = strings.Repeat("b", 32)
	changed[1].MainPID++
	changed[2].ControlGroup = "/user.slice/replacement.service"
	changed[3].Type = "simple"
	for _, current := range changed {
		if sameUnitActivationProperties(got, current) {
			t.Fatalf("changed activation matched: %#v", current)
		}
	}
	if sameUnitActivationProperties(unitActivationProperties{}, unitActivationProperties{}) {
		t.Fatal("empty observations matched")
	}
}

func TestUnitActivationPropertiesRejectAmbiguousOrInvalidData(t *testing.T) {
	base := activationPropertyFixture()
	cases := map[string]string{
		"missing":          strings.ReplaceAll(base, "Type=exec\n", ""),
		"duplicate":        base + "MainPID=4321\n",
		"duplicate change": base + "InvocationID=" + strings.Repeat("a", 32) + "\n",
		"unrelated":        base + "Trusted=yes\n",
		"malformed":        base + "broken\n",
		"zero invocation":  strings.ReplaceAll(base, "0123456789abcdef0123456789abcdef", strings.Repeat("0", 32)),
		"upper invocation": strings.ReplaceAll(base, "0123456789abcdef0123456789abcdef", strings.Repeat("A", 32)),
		"short invocation": strings.ReplaceAll(base, "0123456789abcdef0123456789abcdef", "a"),
		"zero pid":         strings.ReplaceAll(base, "4321", "0"),
		"negative pid":     strings.ReplaceAll(base, "4321", "-1"),
		"overflow pid":     strings.ReplaceAll(base, "4321", "2147483648"),
		"leading zero":     strings.ReplaceAll(base, "4321", "04321"),
		"root cgroup":      strings.ReplaceAll(base, "/user.slice/session.service", "/"),
		"relative cgroup":  strings.ReplaceAll(base, "/user.slice/session.service", "user.slice/session.service"),
		"unclean cgroup":   strings.ReplaceAll(base, "/user.slice/session.service", "/user.slice/../other"),
		"nul cgroup":       strings.ReplaceAll(base, "/user.slice/session.service", "/user.slice/\x00other"),
		"cr cgroup":        strings.ReplaceAll(base, "/user.slice/session.service", "/user.slice/other\r"),
		"unknown type":     strings.ReplaceAll(base, "Type=exec", "Type=unknown"),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := decodeUnitActivationProperties([]byte(raw))
			if err == nil || got != (unitActivationProperties{}) || !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("invalid properties accepted or partial state returned: %#v, %v", got, err)
			}
		})
	}
}

func TestUnitActivationPropertiesAllowReorderingAndNativeValues(t *testing.T) {
	raw := "Type=notify-reload\nControlGroup=/user.slice/a space.service\n\nMainPID=2147483647\nInvocationID=ffffffffffffffffffffffffffffffff\n"
	got, err := decodeUnitActivationProperties([]byte(raw))
	if err != nil || got.MainPID != 2147483647 || got.Type != "notify-reload" {
		t.Fatalf("valid manager values rejected: %#v, %v", got, err)
	}
}

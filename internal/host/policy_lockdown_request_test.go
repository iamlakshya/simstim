package host

import "testing"

func TestLockdownRequestNeverAliasesMissingOrUnknownDataWithOff(t *testing.T) {
	if args, err := DecodePolicyLockdownV1([]byte(`{"v":1,"on":false}`)); err != nil || args.On {
		t.Fatal("literal false host setting refused", err)
	}
	for _, raw := range []string{`{}`, `null`, `{"v":1}`, `{"v":1,"on":null}`, `{"v":1,"On":false}`, `{"v":1,"on":false,"on":true}`, `{"v":1,"on":false,"session":null}`, `{"v":1,"on":false,"previous_hash":null}`, `{"v":1,"on":false,"future":{}}`, `{"v":1,"on":false,"session":"root2345"}`} {
		if _, err := DecodePolicyLockdownV1([]byte(raw)); err == nil {
			t.Fatal("ambiguous request could remove a native restriction", raw)
		}
	}
}

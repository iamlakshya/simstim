package vmconfig

import (
	"encoding/json"
	"github.com/iamlakshya/simstim/internal/proto"
	"strings"
	"testing"
)

func TestVMPrepareCorrelationIsClosedBoundedAndOwned(t *testing.T) {
	a := validVMPrepare()
	a.Correlation = &proto.VMPrepareCorrelationV1{Version: 1, Session: a.Session, Generation: strings.Repeat("b", 32), NeedSHA256: strings.Repeat("c", 64), Nonce: strings.Repeat("d", 32)}
	encoded, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	checked, err := Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	a.Correlation.Session = "changed"
	if checked.Args.Correlation.Session != "abcdefgh" {
		t.Fatal("caller mutated validated correlation")
	}
	for _, bad := range []string{
		strings.Replace(string(encoded), `"correlation":{`, `"correlation":null,"other":{`, 1),
		strings.Replace(string(encoded), `"v":1`, `"v":1,"v":1`, 1),
		strings.Replace(string(encoded), `"v":1`, `"V":1`, 1),
		strings.Replace(string(encoded), `"generation":"`+strings.Repeat("b", 32)+`"`, `"generation":"short"`, 1),
		strings.Replace(string(encoded), `"need_sha256":"`+strings.Repeat("c", 64)+`"`, `"need_sha256":"`+strings.Repeat("C", 64)+`"`, 1),
		strings.Replace(string(encoded), `"nonce":"`+strings.Repeat("d", 32)+`"`, `"nonce":null`, 1),
		strings.Replace(string(encoded), `"correlation":{`, `"correlation":{"operation":true,`, 1),
	} {
		if _, err := Decode([]byte(bad)); err == nil {
			t.Fatal("invalid correlation accepted", bad)
		}
	}
}

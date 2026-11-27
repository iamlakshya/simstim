package api

import (
	"fmt"
	"strings"
	"testing"
)

func TestDecoyTripwireOwnsMutableListsAndRedactsAllFormatting(t *testing.T) {
	input := []DecoyTripwire{{Name: "aws", Values: []Secret{"private-decoy-value"}, Hosts: []string{"sts.amazonaws.com"}}}
	output := CloneDecoyTripwires(input)
	output[0].Values[0], output[0].Hosts[0] = "changed", "changed"
	if input[0].Values[0] != "private-decoy-value" || input[0].Hosts[0] != "sts.amazonaws.com" {
		t.Fatal("decoy clone borrowed a mutable list")
	}
	for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
		if strings.Contains(fmt.Sprintf(format, input[0]), "private-decoy-value") {
			t.Fatal("decoy formatting exposed generated material")
		}
	}
}

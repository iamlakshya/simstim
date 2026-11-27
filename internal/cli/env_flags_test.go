package cli

import (
	"reflect"
	"testing"

	"github.com/spf13/pflag"

	"github.com/iamlakshya/simstim/internal/client"
)

func TestEnvironmentFlagsKeepLiteralValuesAndEncounterOrder(t *testing.T) {
	var options []client.EnvironmentOption
	f := pflag.NewFlagSet("environment", pflag.ContinueOnError)
	addEnvironmentFlags(f, &options)
	if err := f.Parse([]string{"--env", "A=one,two", "--env-file", "vars=a,b", "--env", "A=last=value", "--env", "FROM_LAPTOP"}); err != nil {
		t.Fatal(err)
	}
	want := []client.EnvironmentOption{{Value: "A=one,two"}, {Value: "vars=a,b", File: true}, {Value: "A=last=value"}, {Value: "FROM_LAPTOP"}}
	if !reflect.DeepEqual(options, want) {
		t.Fatalf("environment encounters=%+v", options)
	}
}

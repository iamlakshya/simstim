package cli

import (
	"github.com/spf13/pflag"

	"github.com/iamlakshya/simstim/internal/client"
)

type envFlagValue struct {
	file bool
	all  *[]client.EnvironmentOption
}

func (v *envFlagValue) Set(value string) error {
	*v.all = append(*v.all, client.EnvironmentOption{Value: value, File: v.file})
	return nil
}
func (*envFlagValue) String() string { return "" }
func (*envFlagValue) Type() string   { return "string" }

// addEnvironmentFlags lets the ordinary parser retain exact encounter order.
// Values, commas and equals signs stay literal; the same settings owner checks
// all configured secret names before any env-file read or laptop lookup.
func addEnvironmentFlags(flags *pflag.FlagSet, options *[]client.EnvironmentOption) {
	flags.Var(&envFlagValue{all: options}, "env", "set a guest variable as NAME=VALUE, or read NAME from this computer")
	flags.Var(&envFlagValue{all: options, file: true}, "env-file", "read literal guest variables from a local file")
}

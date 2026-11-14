package client

import (
	"fmt"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/egress"
)

// ParseLayerSets applies repeatable key=value overrides through the same
// strict decoder and typed schema as config.toml. Bare strings are convenient
// at the command line; arrays, booleans and numbers keep their TOML types.
func ParseLayerSets(sets []string) (api.LayerValues, error) {
	var all strings.Builder
	for _, set := range sets {
		key, value, ok := strings.Cut(set, "=")
		if !ok || !strings.Contains(key, ".") {
			return api.LayerValues{}, &UserError{Msg: "--set needs a policy key=value", Hint: "Use --set network.unknown=deny.", Code: "bad_set", Kind: KindUsage}
		}
		var probe api.LayerValues
		text := key + " = " + value + "\n"
		md, err := decodeConfig(text, &probe)
		if err != nil {
			text = key + " = " + fmt.Sprintf("%q", value) + "\n"
			md, err = decodeConfig(text, &probe)
		}
		if err != nil || len(md.Undecoded()) > 0 {
			return api.LayerValues{}, &UserError{Msg: "invalid --set " + CleanLine(key), Hint: brand.Name + " config check", Code: "bad_set", Kind: KindUsage}
		}
		all.WriteString(text)
	}
	var values api.LayerValues
	if _, err := decodeConfig(all.String(), &values); err != nil {
		return values, tomlError("--set", all.String(), err)
	}
	if err := egress.ValidateLayerValues(values); err != nil {
		return values, policyError(err)
	}
	return values, nil
}

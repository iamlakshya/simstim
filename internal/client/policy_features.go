package client

import (
	"reflect"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

// PolicyLayerFeature identifies helpers which preserve exact layer references
// and typed settings. An older helper must never silently flatten these rules.
const PolicyLayerFeature = "policy-layer-order-v1"

// CheckPolicyLayerFeature refuses a payload that an older helper could weaken.
// Truly legacy empty or single-session flat rules retain their old path.
func (c *Conn) CheckPolicyLayerFeature(layers []api.Layer) error {
	if err := c.CheckHTTPMethodFeature(layers); err != nil {
		return err
	}
	if c.HasFeature(PolicyLayerFeature) {
		return nil
	}
	for _, layer := range layers {
		if layer.Kind != api.LayerSession || layer.Source != "" || layer.Name != "" || layer.Lock != nil || !reflect.ValueOf(layer.LayerValues).IsZero() {
			return &UserError{Msg: "this host helper cannot preserve policy layer order", Hint: brand.Name + " host upgrade " + CleanLine(c.Host.Name), Code: "helper_old", Kind: KindFailed}
		}
	}
	return nil
}

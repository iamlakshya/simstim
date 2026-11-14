package egress

import (
	"fmt"
	"maps"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// ValidateLayerValues checks the common policy schema without changing it.
// Config checking, --set and the trusted merge all use these constraints.
func ValidateLayerValues(values api.LayerValues) error {
	return visitValues(reflect.ValueOf(values), "", func(key string, v reflect.Value) error {
		if v.Kind() == reflect.Pointer && !v.IsNil() {
			v = v.Elem()
		} else if v.IsZero() {
			return nil
		}
		if v.Kind() == reflect.String {
			s := v.String()
			choices := map[string][]string{
				"sandbox.wall":   {"auto", "none", "process", "gvisor", "microvm"},
				"sandbox.preset": {"locked", "balanced", "open"},
				"sandbox.system": {"reset", "keep"}, "sandbox.paste": {"media", "any"},
				"sandbox.package_managers": {"guarded", "strict", "off"},
				"network.unknown":          {"ask", "deny", "allow", "classify"},
				"classifier.judge":         {"none", "model", "command"},
				"classifier.unsure":        {"ask", "deny"}, "classifier.max_scope": {"once", "session"},
			}
			if allowed, found := choices[key]; found && !slices.Contains(allowed, s) {
				return badLayerValue(key, "must be "+strings.Join(allowed, ", "))
			}
			switch key {
			case "sandbox.disk_max", "sandbox.pull_max", "sandbox.cp_max", "sandbox.copy_max", "sandbox.seed_max", "network.upload_budget":
				if _, err := api.ParseSize(s); err != nil {
					return badLayerValue(key, err.Error())
				}
			case "sandbox.memory":
				if s != "" {
					if _, err := api.ParseMemoryMax(s); err != nil {
						return badLayerValue(key, err.Error())
					}
				}
			case "sandbox.idle_stop", "sandbox.max_run", "sandbox.stop_grace", "network.hold", "network.ask_open", "classifier.timeout":
				if (key == "sandbox.idle_stop" && s == "never") || key == "sandbox.max_run" && s == "" {
					break
				}
				d, err := time.ParseDuration(s)
				if err != nil || d <= 0 || key == "sandbox.stop_grace" && d > time.Minute {
					return badLayerValue(key, "needs a positive duration (stop_grace is at most 60s)")
				}
			case "network.min_release_age":
				n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
				if err != nil || n < 0 || !strings.HasSuffix(s, "d") {
					return badLayerValue(key, "needs whole days, such as 7d")
				}
			case "classifier.endpoint":
				if s != "" {
					u, err := url.Parse(s)
					if err != nil || u.Hostname() == "" || u.Scheme != "https" && u.Scheme != "http" || u.User != nil {
						return badLayerValue(key, "needs an http or https URL without credentials")
					}
				}
			}
		}
		if v.Kind() == reflect.Int || v.Kind() == reflect.Int64 {
			if v.Int() < 0 {
				return badLayerValue(key, "cannot be negative")
			}
		}
		if key == "network.rules" {
			for _, raw := range v.Interface().([]api.NetworkRule) {
				if _, err := CanonicalNetworkRule(raw); err != nil {
					return err
				}
			}
			return nil
		}
		if v.Kind() == reflect.Slice {
			for i := 0; i < v.Len(); i++ {
				s := v.Index(i).String()
				if strings.TrimSpace(strings.TrimPrefix(s, "!")) == "" {
					return badLayerValue(key, "contains an empty item")
				}
				if key == "network.allow" || key == "network.deny" {
					if _, err := ValidateRuleTarget(s); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
}

func badLayerValue(key, message string) error {
	return &proto.Error{Code: "bad_value", Message: key + " " + message, Hint: "Run config check, then correct this setting."}
}

// visitValues visits only the schema's leaf fields, using its TOML tags.
func visitValues(v reflect.Value, prefix string, f func(string, reflect.Value) error) error {
	for i := 0; i < v.NumField(); i++ {
		key := strings.Split(v.Type().Field(i).Tag.Get("toml"), ",")[0]
		if prefix != "" {
			key = prefix + "." + key
		}
		field := v.Field(i)
		if field.Kind() == reflect.Struct {
			if err := visitValues(field, key, f); err != nil {
				return err
			}
			continue
		}
		if err := f(key, field); err != nil {
			return err
		}
	}
	return nil
}

func valuesMap(v api.LayerValues) map[string]reflect.Value {
	out := map[string]reflect.Value{}
	_ = visitValues(reflect.ValueOf(v), "", func(k string, v reflect.Value) error {
		if !v.IsZero() {
			out[k] = v
		}
		return nil
	})
	return out
}

func mergeLayerValues(dst *api.LayerValues, l api.Layer, origins map[string]api.ValueOrigin) {
	incoming := valuesMap(l.LayerValues)
	_ = visitValues(reflect.ValueOf(dst).Elem(), "", func(key string, old reflect.Value) error {
		v, ok := incoming[key]
		if !ok || key == "network.allow" || key == "network.deny" || key == "network.rules" {
			return nil
		}
		switch v.Kind() {
		case reflect.Pointer:
			if key == "sandbox.wall" && !old.IsNil() {
				rank := map[string]int{"auto": 0, "none": 0, "process": 1, "gvisor": 2, "microvm": 3}
				if rank[old.Elem().String()] > rank[v.Elem().String()] {
					return nil
				}
			}
			old.Set(cloneValue(v))
		case reflect.Slice:
			items := slices.Clone(old.Interface().([]string))
			for _, item := range v.Interface().([]string) {
				if remove, drop := strings.CutPrefix(item, "!"); drop {
					items = slices.DeleteFunc(items, func(s string) bool { return s == remove })
					delete(origins, key+"\n"+remove)
				} else if !slices.Contains(items, item) {
					items = append(items, item)
					origins[key+"\n"+item] = api.ValueOrigin{Key: key, Item: item, Layer: l.Kind, Name: l.Name, Source: l.Source}
				}
			}
			old.Set(reflect.ValueOf(items))
			return nil
		case reflect.Map:
			items := maps.Clone(old.Interface().(map[string]string))
			if items == nil {
				items = map[string]string{}
			}
			maps.Copy(items, v.Interface().(map[string]string))
			for name := range v.Interface().(map[string]string) {
				origins[key+"\n"+name] = api.ValueOrigin{Key: key, Item: name, Layer: l.Kind, Name: l.Name, Source: l.Source}
			}
			old.Set(reflect.ValueOf(items))
			return nil
		default:
			old.Set(cloneValue(v))
		}
		origins[key] = api.ValueOrigin{Key: key, Layer: l.Kind, Name: l.Name, Source: l.Source}
		return nil
	})
}

func cloneValue(v reflect.Value) reflect.Value {
	if v.Kind() == reflect.Slice {
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		reflect.Copy(out, v)
		return out
	}
	if v.Kind() == reflect.Map {
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		iter := v.MapRange()
		for iter.Next() {
			out.SetMapIndex(iter.Key(), iter.Value())
		}
		return out
	}
	if v.Kind() != reflect.Pointer {
		return v
	}
	out := reflect.New(v.Type().Elem())
	out.Elem().Set(v.Elem())
	return out
}

// canonicalLayerValues also accepts the original preset and unknown wire
// fields. A conflict is refused rather than choosing a spelling silently.
func canonicalLayerValues(l *api.Layer) error {
	if err := ValidateLayerValues(l.LayerValues); err != nil {
		return err
	}
	if p := l.Sandbox.Preset; p != nil {
		if l.Preset != "" && string(l.Preset) != *p {
			return badLayerValue("sandbox.preset", "conflicts with preset")
		}
		l.Preset = api.Preset(*p)
	}
	if u := l.Network.Unknown; u != nil {
		if l.Unknown != "" && string(l.Unknown) != *u {
			return badLayerValue("network.unknown", "conflicts with unknown")
		}
		l.Unknown = api.Unknown(*u)
	}
	if l.Preset != "" {
		p := string(l.Preset)
		l.Sandbox.Preset = &p
	}
	if l.Unknown != "" {
		u := string(l.Unknown)
		l.Network.Unknown = &u
	}
	if err := ValidateLayerValues(l.LayerValues); err != nil {
		return err
	}
	for _, target := range l.Network.Allow {
		l.Rules = append(l.Rules, api.Rule{Allow: true, Door: api.DoorNet, Target: target})
	}
	for _, target := range l.Network.Deny {
		l.Rules = append(l.Rules, api.Rule{Door: api.DoorNet, Target: target})
	}
	if err := convertNetworkRules(l); err != nil {
		return err
	}
	if l.Source == api.SourceRepositoryFile {
		for _, rule := range l.Rules {
			if rule.Allow {
				return &proto.Error{Code: "code_grant", Message: "the repository file proposes an allowance", Hint: "Review the repository file's grants before starting."}
			}
		}
		if l.Lock != nil {
			return &proto.Error{Code: "layer_key", Message: "the repository file cannot set locks", Hint: "Put locks in config.toml or the host's entry."}
		}
	}
	return nil
}

func layerError(l api.Layer, key string, lock api.Lock) error {
	return &proto.Error{Code: "policy_locked", Message: fmt.Sprintf("%s %q cannot change %s, locked by %s %q", l.Kind, l.Name, key, lock.Layer, lock.Name), Hint: "Review the named lock in config.toml or hosts.toml."}
}

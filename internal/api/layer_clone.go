package api

import "reflect"

// CloneLayers copies all mutable source data, including nested typed settings,
// locks, rules, maps and pointer values. Strings and time instants are values.
func CloneLayers(layers []Layer) []Layer {
	if layers == nil {
		return nil
	}
	return cloneLayerData(reflect.ValueOf(layers)).Interface().([]Layer)
}

// CloneLayerValues copies the mutable settings of one layer.
func CloneLayerValues(values LayerValues) LayerValues {
	return cloneLayerData(reflect.ValueOf(values)).Interface().(LayerValues)
}

func cloneLayerData(value reflect.Value) reflect.Value {
	switch value.Kind() {
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		out := reflect.New(value.Type().Elem())
		out.Elem().Set(cloneLayerData(value.Elem()))
		return out
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		out := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for i := range value.Len() {
			out.Index(i).Set(cloneLayerData(value.Index(i)))
		}
		return out
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		out := reflect.MakeMapWithSize(value.Type(), value.Len())
		iter := value.MapRange()
		for iter.Next() {
			out.SetMapIndex(iter.Key(), cloneLayerData(iter.Value()))
		}
		return out
	case reflect.Struct:
		out := reflect.New(value.Type()).Elem()
		out.Set(value)
		for i := range value.NumField() {
			if value.Type().Field(i).PkgPath == "" {
				out.Field(i).Set(cloneLayerData(value.Field(i)))
			}
		}
		return out
	default:
		return value
	}
}

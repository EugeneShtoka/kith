package config

import "reflect"

// Clone returns a deep copy of c, safe to edit while c is encoded on another goroutine.
func (c Config) Clone() Config {
	out, _ := reflect.TypeAssert[Config](deepCopy(reflect.ValueOf(c)))
	return out
}

// deepCopy copies v recursively; a struct with unexported fields (time.Time) is copied
// whole.
func deepCopy(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Slice:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := range v.Len() {
			out.Index(i).Set(deepCopy(v.Index(i)))
		}
		return out
	case reflect.Map:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		for it := v.MapRange(); it.Next(); {
			out.SetMapIndex(it.Key(), deepCopy(it.Value()))
		}
		return out
	case reflect.Pointer:
		if v.IsNil() {
			return v
		}
		out := reflect.New(v.Type().Elem())
		out.Elem().Set(deepCopy(v.Elem()))
		return out
	case reflect.Interface:
		if v.IsNil() {
			return v
		}
		out := reflect.New(v.Type()).Elem()
		out.Set(deepCopy(v.Elem()))
		return out
	case reflect.Struct:
		out := reflect.New(v.Type()).Elem()
		for i := range v.NumField() {
			if !v.Type().Field(i).IsExported() {
				out.Set(v)
				return out
			}
			out.Field(i).Set(deepCopy(v.Field(i)))
		}
		return out
	default:
		return v
	}
}

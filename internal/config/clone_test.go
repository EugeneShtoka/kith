package config

import (
	"reflect"
	"testing"
)

// Every slice, map and pointer reachable from a clone is its own: writing through any
// of them leaves the original alone. The walk covers every field, so a new one is
// checked without this test changing.
func TestCloneSharesNothing(t *testing.T) {
	t.Parallel()

	var orig Config
	fill(reflect.ValueOf(&orig).Elem())
	clone := orig.Clone()
	if !reflect.DeepEqual(orig, clone) {
		t.Fatal("clone differs from the original")
	}
	if n := shared(reflect.ValueOf(orig), reflect.ValueOf(clone), "Config"); n != "" {
		t.Fatalf("clone shares %s with the original", n)
	}
}

// fill gives every slice and map one element and every pointer a value, all the way
// down, so there is something to share.
func fill(v reflect.Value) {
	switch v.Kind() {
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fill(v.Index(0))
	case reflect.Map:
		v.Set(reflect.MakeMap(v.Type()))
		key := reflect.New(v.Type().Key()).Elem()
		fill(key)
		val := reflect.New(v.Type().Elem()).Elem()
		fill(val)
		v.SetMapIndex(key, val)
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fill(v.Elem())
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				fill(v.Field(i))
			}
		}
	case reflect.String:
		v.SetString("x")
	}
}

// shared names the first slice, map or pointer a and b both point at, or "".
func shared(a, b reflect.Value, path string) string {
	switch a.Kind() {
	case reflect.Slice, reflect.Map, reflect.Pointer:
		if !a.IsNil() && a.Pointer() == b.Pointer() {
			return path
		}
	}
	switch a.Kind() {
	case reflect.Slice:
		for i := range a.Len() {
			if n := shared(a.Index(i), b.Index(i), path+"[]"); n != "" {
				return n
			}
		}
	case reflect.Map:
		for it := a.MapRange(); it.Next(); {
			if n := shared(it.Value(), b.MapIndex(it.Key()), path+"{}"); n != "" {
				return n
			}
		}
	case reflect.Pointer:
		if !a.IsNil() {
			return shared(a.Elem(), b.Elem(), path)
		}
	case reflect.Struct:
		for i := range a.NumField() {
			if a.Type().Field(i).IsExported() {
				if n := shared(a.Field(i), b.Field(i), path+"."+a.Type().Field(i).Name); n != "" {
					return n
				}
			}
		}
	}
	return ""
}

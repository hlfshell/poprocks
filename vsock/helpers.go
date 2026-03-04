package vsock

import (
	"io"
	"reflect"
)

func isStreamType(t reflect.Type) bool {
	readerType := reflect.TypeFor[io.Reader]()
	readSeekerType := reflect.TypeFor[io.ReadSeeker]()
	streamSourceType := reflect.TypeFor[StreamSource]()
	return typeImplements(t, readerType) ||
		typeImplements(t, readSeekerType) ||
		typeImplements(t, streamSourceType)
}

func typeImplements(t, iface reflect.Type) bool {
	if t == nil || iface == nil {
		return false
	}
	if t.Implements(iface) {
		return true
	}
	if t.Kind() != reflect.Ptr && reflect.PointerTo(t).Implements(iface) {
		return true
	}
	return false
}

func hasStructTag(t reflect.Type, tagName string, seen map[reflect.Type]bool) bool {
	if t.Kind() != reflect.Struct {
		return false
	}
	if seen[t] {
		return false
	}
	seen[t] = true

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.Tag.Get(tagName) != "" {
			return true
		}
		ft := field.Type
		for ft.Kind() == reflect.Ptr {
			ft = ft.Elem()
		}
		if hasStructTag(ft, tagName, seen) {
			return true
		}
	}
	return false
}

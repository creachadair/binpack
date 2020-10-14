// Copyright (C) 2020 Michael J. Fromberger. All Rights Reserved.

package binpack

import (
	"bytes"
	"encoding"
	"encoding/binary"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// A BinpackMarshaler encodes a value for use as a binpack value.
type BinpackMarshaler interface {
	MarshalBinpack() ([]byte, error)
}

// Marshal encodes v as a binary value for a binpack tag-value pair.
// If v implements BinpackMarshaler, its MarshalBinpack method is called.
// Otherwise, if v implements encoding.BinaryMarshaler, that method is called.
//
// For struct types, Marshal uses field tags to select which exported fields
// should be included and to assign them tag values. The tag format is:
//
//     binpack:"tag=n"
//
// where n is an unsigned integer value. Zero-valued fields are not encoded.
// Fields of slice types other than []byte are encoded inline, unless the
// "pack" attribute is also set, for example:
//
//     Names []string `binpack:"tag=24,pack"`
//
// When "pack" is set, the slice is instead packed into a single value.  This
// generally makes sense for small values.
//
// Note that map values are encoded in iteration order, which means that
// marshaling a value that is or contains a map may not be deterministic.
// Other than maps, however, the output is deterministic.
func Marshal(v interface{}) ([]byte, error) {
	switch t := v.(type) {
	case BinpackMarshaler:
		return t.MarshalBinpack()
	case encoding.BinaryMarshaler:
		return t.MarshalBinary()
	case byte: // handles uint8
		return []byte{t}, nil
	case []byte:
		return t, nil
	case string:
		return []byte(t), nil
	case bool:
		if t {
			return []byte{1}, nil
		}
		return []byte{0}, nil
	case nil:
		return []byte{0}, nil
	}
	if ok, buf := marshalNumber(v); ok {
		return buf, nil
	}
	isNilPtr, val := deref(v)
	if isNilPtr {
		return []byte{0}, nil // placeholder for nil
	}
	if typ := val.Type(); typ.Kind() == reflect.Slice {
		return marshalSlice(val)
	} else if typ.Kind() == reflect.Struct {
		return marshalStruct(val)
	} else if typ.Kind() == reflect.Map {
		return marshalMap(val)
	}
	return nil, fmt.Errorf("type %T cannot be marshaled", v)
}

// marshalNumber reports whether v is one of the built-in numeric types, apart
// from byte and uint8; if so it also returns the encoding of v.
func marshalNumber(v interface{}) (bool, []byte) {
	var z uint64
	switch t := v.(type) {
	case uint16:
		z = uint64(t)
	case uint32:
		z = uint64(t)
	case uint64:
		z = t
	case int:
		w := int64(t)
		z = uint64(w<<1) ^ uint64(w>>63) // zigzag
	case int8:
		z = uint64(t<<1) ^ uint64(t>>7) // zigzag
	case int16:
		z = uint64(t<<1) ^ uint64(t>>15) // zigzag
	case int32:
		z = uint64(t<<1) ^ uint64(t>>31) // zigzag
	case int64:
		z = uint64(t<<1) ^ uint64(t>>63) // zigzag
	case float32:
		z = uint64(math.Float32bits(t))
	case float64:
		z = math.Float64bits(t)
	default:
		return false, nil
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], z)
	for i, b := range buf {
		if b != 0 {
			return true, buf[i:]
		}
	}
	return true, buf[:1]
}

// deref reports whether v is nil pointer.  If v a non-nil pointer, it returns
// the reflect.Value corresponding to its pointee; v is not a pointer and it
// returns v itself.
func deref(v interface{}) (bool, reflect.Value) {
	val := reflect.ValueOf(v)
	if val.Type().Kind() == reflect.Ptr {
		if val.IsNil() {
			return true, val
		}
		return false, val.Elem()
	}
	return false, val
}

// marshalSlice encodes a slice as a single tag-value pair whose tag is the
// length of the slice and value is the concatenation of its encoded values.
// Precondition: val is a reflect.Slice.
func marshalSlice(val reflect.Value) ([]byte, error) {
	var buf bytes.Buffer
	writeTag(&buf, val.Len())
	for i := 0; i < val.Len(); i++ {
		cur := val.Index(i).Interface()
		data, err := Marshal(cur)
		if err != nil {
			return nil, fmt.Errorf("marshaling index %d: %w", i, err)
		}
		writeValue(&buf, data)
	}
	return buf.Bytes(), nil
}

// marshalMap encodes a map as a single tag-value pair whose tag is the number
// of entries in the map and value is the concatenation of its encoded
// key/value pairs in consecutive order.
// Precondition: val is a reflect.Map.
func marshalMap(val reflect.Value) ([]byte, error) {
	var buf bytes.Buffer
	writeTag(&buf, val.Len())
	for _, key := range val.MapKeys() {
		data, err := Marshal(key.Interface())
		if err != nil {
			return nil, err
		}
		writeValue(&buf, data)
		kval := val.MapIndex(key)
		switch kval.Type().Kind() {
		case reflect.Ptr, reflect.Interface:
			if kval.IsNil() {
				data = []byte{0}
				break
			}
			fallthrough
		default:
			data, err = Marshal(kval.Interface())
		}
		if err != nil {
			return nil, fmt.Errorf("got here: %v", err)
		}
		writeValue(&buf, data)
	}
	return buf.Bytes(), nil
}

// marshalStruct encodes a struct as a sequence of tag-value pairs.
// Precondition: val is a reflect.Struct.
func marshalStruct(val reflect.Value) ([]byte, error) {
	info, err := checkStructType(val, false /* no pointers */)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	put := func(tag int, data []byte) {
		writeTag(&buf, tag)
		writeValue(&buf, data)
	}

	for _, fi := range info {
		// Slice fields are flattened into the stream unless packed.
		if fi.slice && !fi.pack {
			for i, elt := range fi.target.([]interface{}) {
				data, err := Marshal(elt)
				if err != nil {
					return nil, fmt.Errorf("index %d: %w", i, err)
				}
				put(fi.tag, data)
			}
			continue
		} else if data, err := Marshal(fi.target); err != nil {
			return nil, err
		} else {
			put(fi.tag, data)
		}
	}
	return buf.Bytes(), nil
}

// checkStructType extracts a field map from a struct type.
// Precondition: val is a reflect.Struct.
func checkStructType(val reflect.Value, withPointer bool) ([]*fieldInfo, error) {
	var info []*fieldInfo
	for i := 0; i < val.NumField(); i++ {
		ftype := val.Type().Field(i)
		tag, ok := ftype.Tag.Lookup("binpack")
		if !ok {
			continue
		}
		fi, ok := parseTag(tag)
		if !ok {
			return nil, fmt.Errorf("invalid field %q tag %q", ftype.Name, tag)
		}

		field := val.Field(i)
		fi.slice = field.Kind() == reflect.Slice
		if withPointer {
			if field.Kind() == reflect.Ptr {
				p := reflect.New(field.Type().Elem())
				field.Set(p)
				fi.target = p.Interface()
			} else if !field.CanAddr() {
				return nil, fmt.Errorf("field %q cannot be addressed", ftype.Name)
			} else {
				fi.target = field.Addr().Interface()
			}
		} else if field.IsZero() {
			continue
		} else if fi.slice {
			var vals []interface{}
			for i := 0; i < field.Len(); i++ {
				vals = append(vals, field.Index(i).Interface())
			}
			fi.target = vals
		} else {
			fi.target = field.Interface()
		}
		info = append(info, &fi)
	}
	sort.Slice(info, func(i, j int) bool {
		return info[i].tag < info[j].tag
	})

	// Check for duplicate tags.
	for i := 0; i < len(info)-1; i++ {
		if info[i].tag == info[i+1].tag {
			return nil, fmt.Errorf("duplicate field tag %d", info[i].tag)
		}
	}
	return info, nil
}

type fieldInfo struct {
	tag    int
	slice  bool
	pack   bool
	target interface{}
}

func parseTag(s string) (fieldInfo, bool) {
	var fi fieldInfo
	for _, arg := range strings.Split(s, ",") {
		if arg == "pack" {
			fi.pack = true
		} else if strings.HasPrefix(arg, "tag=") {
			v, err := strconv.Atoi(arg[4:])
			if err != nil {
				return fi, false
			}
			fi.tag = v
		}
	}
	return fi, true
}

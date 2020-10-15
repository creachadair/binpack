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

// A Marshaler encodes a value into an array of bytes for use in a binpack
// tag-value pair.
type Marshaler interface {
	MarshalBinpack() ([]byte, error)
}

// Marshal encodes v as a binary value for a binpack tag-value pair.
// If v implements Marshaler, its MarshalBinpack method is called.  Otherwise,
// if v implements encoding.BinaryMarshaler, that method is called.
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
	case Marshaler:
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

// marshalSlice encodes a slice as a concatenated sequence of values.
// Precondition: val is a reflect.Slice.
func marshalSlice(val reflect.Value) ([]byte, error) {
	vals, err := packSlice(val)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	for _, elt := range vals {
		writeValue(&buf, elt)
	}
	return buf.Bytes(), nil
}

// packSlice encodes a slice into a slice of byte records.
// Precondition: val is a reflect.Slice.
func packSlice(val reflect.Value) ([][]byte, error) {
	var vals [][]byte
	for i := 0; i < val.Len(); i++ {
		cur := val.Index(i).Interface()
		data, err := Marshal(cur)
		if err != nil {
			return nil, fmt.Errorf("marshaling index %d: %w", i, err)
		}
		vals = append(vals, data)
	}
	return vals, nil
}

// marshalMap encodes a map as a concatenated sequence of key-value pairs.
// Note that iteration order affects the output, and may vary.
// Precondition: val is a reflect.Map.
func marshalMap(val reflect.Value) ([]byte, error) {
	vals, err := packMap(val)
	if err != nil {
		return nil, err
	}
	return marshalSlice(reflect.ValueOf(vals))
}

// packMap encodes a map as a slice of byte records.
// Precondition: val is a reflect.Map.
func packMap(val reflect.Value) ([][]byte, error) {
	var vals [][]byte
	for _, key := range val.MapKeys() {
		var buf bytes.Buffer
		if bits, err := Marshal(key.Interface()); err != nil {
			return nil, err
		} else {
			writeValue(&buf, bits)
		}
		if bits, err := Marshal(val.MapIndex(key).Interface()); err != nil {
			return nil, err
		} else {
			writeValue(&buf, bits)
		}
		vals = append(vals, buf.Bytes())
	}
	return vals, nil
}

// marshalStruct encodes a struct as a sequence of tag-value pairs.
// Precondition: val is a reflect.Struct.
func marshalStruct(val reflect.Value) ([]byte, error) {
	info, err := checkStructType(val, false /* no pointers */)
	if err != nil {
		return nil, err
	}
	buf := NewBuffer(nil)

	for _, fi := range info {
		// Slice fields are flattened into the stream unless packed.
		if fi.seq && !fi.pack {
			for i, elt := range fi.target.([][]byte) {
				data, err := Marshal(elt)
				if err != nil {
					return nil, fmt.Errorf("index %d: %w", i, err)
				}
				buf.Encode(fi.tag, data)
			}
			continue
		} else if data, err := Marshal(fi.target); err != nil {
			return nil, err
		} else {
			buf.Encode(fi.tag, data)
		}
	}
	return buf.Data.Bytes(), nil
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
		kind := field.Kind()
		fi.seq = kind == reflect.Slice || kind == reflect.Map
		if withPointer {
			// If the caller wants a writable value, ensure the target is a pointer.
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
			// The caller is encoding; skip zero values.
			continue

		} else if kind == reflect.Slice {
			// The caller is encoding; package slice values into a slice.
			vals, err := packSlice(field)
			if err != nil {
				return nil, err
			}
			fi.target = vals

		} else if kind == reflect.Map {
			// The caller is encoding; package map entries into a slice.
			vals, err := packMap(field)
			if err != nil {
				return nil, err
			}
			fi.target = vals

		} else {
			// THe caller is encoding; this is a singleton.
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
	tag    int  // field tag
	seq    bool // value is a sequence (slice or map)
	pack   bool // use packed encoding
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

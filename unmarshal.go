// Copyright (C) 2020 Michael J. Fromberger. All Rights Reserved.

package binpack

import (
	"bytes"
	"encoding"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
)

// Unmarshal decodes data from binpack format into v.
// If v implements encoding.BinaryUnmarshaler, that method is called.
//
// Because the binpack format does not record type information, unmarshaling
// into an untyped interface will produce the input data unmodified.
func Unmarshal(data []byte, v interface{}) error {
	switch t := v.(type) {
	case encoding.BinaryUnmarshaler:
		return t.UnmarshalBinary(data)
	case *byte:
		b, ok := oneByte(data)
		if !ok {
			return errors.New("invalid encoding of byte")
		}
		*t = b
		return nil
	case *[]byte:
		*t = copyOf(data)
		return nil
	case *interface{}:
		*t = copyOf(data)
	case *string:
		*t = string(data)
		return nil
	case *bool:
		b, ok := oneByte(data)
		if !ok {
			return errors.New("invalid encoding of bool")
		}
		*t = b != 0
		return nil
	case nil:
		return errors.New("cannot unmarshal into nil")
	}
	if ok, err := unmarshalNumber(data, v); ok {
		return err
	}
	val := reflect.ValueOf(v)
	if typ := val.Type(); typ.Kind() != reflect.Ptr {
		return fmt.Errorf("non-pointer %T cannot be unmarshaled", v)
	} else if val.IsNil() {
		return fmt.Errorf("cannot unmarshal into a nil %T", v)
	} else if typ.Elem().Kind() == reflect.Ptr {
		// Pointer-to-pointer.
		p := reflect.New(typ.Elem().Elem())
		if err := Unmarshal(data, p.Interface()); err != nil {
			return err
		}
		val.Elem().Set(p)
		return nil
	}
	if kind := val.Elem().Type().Kind(); kind == reflect.Slice {
		return unmarshalSlice(data, val)
	} else if kind == reflect.Struct {
		return unmarshalStruct(data, val)
	} else if kind == reflect.Map {
		return unmarshalMap(data, val)
	}
	return fmt.Errorf("type %T cannot be unmarshaled", v)
}

// oneByte reports whether data has length 1, and if so returns that byte.
func oneByte(data []byte) (byte, bool) {
	if len(data) != 1 {
		return 0, false
	}
	return data[0], true
}

// unmarshalNumber reports whether v is a pointer to one of the built-in
// numeric types, apart from byte and uint8; if so it also populates v with the
// decoding.
func unmarshalNumber(data []byte, v interface{}) (bool, error) {
	switch t := v.(type) {
	case *uint16:
		*t = uint16(UnpackUint64(data))
	case *uint32:
		*t = uint32(UnpackUint64(data))
	case *uint64:
		*t = uint64(UnpackUint64(data))
	case *int:
		*t = int(UnpackInt64(data))
	case *int8:
		*t = int8(UnpackInt64(data))
	case *int16:
		*t = int16(UnpackInt64(data))
	case *int32:
		*t = int32(UnpackInt64(data))
	case *int64:
		*t = int64(UnpackInt64(data))
	case *float32:
		*t = math.Float32frombits(uint32(UnpackUint64(data)))
	case *float64:
		*t = math.Float64frombits(UnpackUint64(data))
	default:
		return false, nil
	}

	// N.B. We don't do this check till we know the target was actually a
	// numeric type, since this might be fine for some other value.
	if len(data) == 0 || len(data) > 8 {
		return true, errors.New("invalid number encoding")
	}
	return true, nil
}

func copyOf(data []byte) []byte {
	out := make([]byte, len(data))
	copy(out, data)
	return out
}

func newElement(etype reflect.Type) (reflect.Value, bool) {
	if etype.Kind() == reflect.Ptr {
		return reflect.New(etype.Elem()), true
	}
	return reflect.New(etype), false
}

// unpackElement decodes a single value and appends it to a slice.
// Precondition: val is a pointer to a reflect.Slice.
func unpackElement(element []byte, val reflect.Value) error {
	if val.IsZero() {
		val.Set(reflect.New(val.Elem().Type()))
	}
	etype := val.Elem().Type().Elem()
	elt, isPtr := newElement(etype)
	if err := Unmarshal(element, elt.Interface()); err != nil {
		return err
	}
	if !isPtr {
		elt = elt.Elem()
	}
	val.Elem().Set(reflect.Append(val.Elem(), elt))
	return nil
}

// unmarshalSlice decodes into a slice from a packed array. The values are
// appended to the current contents of val.
// Precondition: val is a pointer to a reflect.Slice.
func unmarshalSlice(data []byte, val reflect.Value) error {
	buf := bytes.NewReader(data)
	for {
		next, err := readValue(buf)
		if err == io.EOF {
			break
		} else if err != nil {
			return err
		}
		if err := unpackElement(next, val); err != nil {
			return err
		}
	}
	return nil
}

// unpackEntry decodes an entry and adds the key/value pair to val.
// Precondition: val is a pointer to a reflect.Value.
func unpackEntry(entry []byte, val reflect.Value) error {
	out := val.Elem()
	if out.IsNil() {
		out.Set(reflect.MakeMap(out.Type()))
	}
	ktype := out.Type().Key()
	vtype := out.Type().Elem()

	ebuf := bytes.NewReader(entry)
	kdata, err := readValue(ebuf)
	if err != nil {
		return fmt.Errorf("map key: %w", err)
	}
	vdata, err := readValue(ebuf)
	if err != nil {
		return fmt.Errorf("map value: %w", err)
	}
	if v, err := readValue(ebuf); err != io.EOF {
		return fmt.Errorf("extra data in map entry: %q", string(v))
	}
	mkey := reflect.New(ktype)
	if err := Unmarshal(kdata, mkey.Interface()); err != nil {
		return err
	}
	mval := reflect.New(vtype)
	if err := Unmarshal(vdata, mval.Interface()); err != nil {
		return err
	}
	out.SetMapIndex(mkey.Elem(), mval.Elem())
	return nil
}

// unmarshalMap decodes a map from a sequence of values representing pairs of
// map keys and values in sequence.
func unmarshalMap(data []byte, val reflect.Value) error {
	mtype := val.Elem().Type()
	if val.IsNil() {
		val.Set(reflect.New(mtype))
	}
	if val.Elem().IsNil() {
		val.Elem().Set(reflect.MakeMap(mtype))
	}

	buf := bytes.NewReader(data)
	for {
		entry, err := readValue(buf)
		if err == io.EOF {
			break
		} else if err != nil {
			return err
		}
		if err := unpackEntry(entry, val); err != nil {
			return err
		}
	}
	return nil
}

// unmarshalStruct decodes a struct from a sequence of tag-value pairs.
// Precondition: val is a non-nil pointer to a reflect.Struct.
func unmarshalStruct(data []byte, val reflect.Value) error {
	info, err := checkStructType(val.Elem(), true /* pointers */)
	if err != nil {
		return err
	}
	find := func(tag int) *fieldInfo {
		for _, fi := range info {
			if fi.tag == tag {
				return fi
			}
		}
		return nil
	}

	d := NewDecoder(bytes.NewReader(data))
	for {
		tag, data, err := d.Decode()
		if err == io.EOF {
			break
		} else if err != nil {
			return err
		}
		fi := find(tag)
		if fi == nil {
			continue // skip unknown fields
		}

		// Non-sequence.
		if !fi.seq {
			if err := Unmarshal(data, fi.target.Interface()); err != nil {
				return err
			}
			continue
		}
		slc := fi.target
		kind := slc.Type().Elem().Kind()

		// Inline sequence element
		switch kind {
		case reflect.Map:
			if err := unpackEntry(data, slc); err != nil {
				return err
			}

		case reflect.Slice:
			if slc.IsNil() {
				slc.Set(reflect.New(slc.Elem().Type()))
			}
			elt, isPtr := newElement(slc.Elem().Type().Elem())
			if err := Unmarshal(data, elt.Interface()); err != nil {
				return err
			}
			if !isPtr {
				elt = elt.Elem()
			}
			slc.Elem().Set(reflect.Append(slc.Elem(), elt))
		}
	}
	return nil
}

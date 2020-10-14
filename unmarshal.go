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

// A BinpackUnmarshaler decodes a binpack value into the receiver.
type BinpackUnmarshaler interface {
	UnmarshalBinpack([]byte) error
}

// Unmarshal decodes data from binpack format into v.
// If v implements BinpackUnmarshaler, its UnmarshalBinpack method is called.
// Otherwise, if v implements encoding.BinaryUnmarshaler, that method is used.
//
// Because the binpack format does not record type information, unmarshaling
// into an untyped interface will produce the input data unmodified.
func Unmarshal(data []byte, v interface{}) error {
	switch t := v.(type) {
	case BinpackUnmarshaler:
		return t.UnmarshalBinpack(data)
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
	if val.Type().Kind() != reflect.Ptr {
		return fmt.Errorf("non-pointer %T cannot be unmarshaled", v)
	} else if val.IsNil() {
		return fmt.Errorf("cannot unmarshal into a nil %T", v)
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
	var z uint64
	for _, b := range data {
		z = (z << 8) | uint64(b)
	}
	mask := math.MaxUint64 + (1 - z&1)

	switch t := v.(type) {
	case *uint16:
		*t = uint16(z)
	case *uint32:
		*t = uint32(z)
	case *uint64:
		*t = uint64(z)
	case *int:
		w := int64(mask ^ z>>1)
		*t = int(w)
	case *int8:
		*t = int8(mask ^ z>>1)
	case *int16:
		*t = int16(mask ^ z>>1)
	case *int32:
		*t = int32(mask ^ z>>1)
	case *int64:
		*t = int64(mask ^ z>>1)
	case *float32:
		*t = math.Float32frombits(uint32(z))
	case *float64:
		*t = math.Float64frombits(z)
	default:
		return false, nil
	}
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

// unmarshalSlice decodes into a slice from a packed array. The values are
// appended to the current contents of val.
// Precondition: val is a pointer to a reflect.Slice.
func unmarshalSlice(data []byte, val reflect.Value) error {
	if val.IsZero() {
		val.Set(reflect.New(val.Elem().Type()))
	}
	buf := bytes.NewReader(data)
	size, err := readTag(buf)
	if err != nil {
		return fmt.Errorf("invalid array size: %w", err)
	}
	if val.Elem().Len() == 0 {
		val.Elem().Set(reflect.MakeSlice(val.Elem().Type(), 0, size))
	}

	etype := val.Elem().Type().Elem()
	for {
		next, err := readValue(buf)
		if err == io.EOF {
			break
		} else if err != nil {
			return err
		}

		elt, isPtr := newElement(etype)
		if err := Unmarshal(next, elt.Interface()); err != nil {
			return err
		}
		if !isPtr {
			elt = elt.Elem()
		}
		val.Elem().Set(reflect.Append(val.Elem(), elt))
		size--
	}
	if size != 0 {
		return errors.New("invalid packed array")
	}
	return nil
}

// unmarshalMap decodes a map from a sequence of values representing pairs of
// map keys and values in sequence.
func unmarshalMap(data []byte, val reflect.Value) error {
	mtype := val.Elem().Type()
	if val.IsNil() {
		val.Set(reflect.New(mtype))
	}
	buf := bytes.NewReader(data)
	size, err := readTag(buf)
	if err != nil {
		return fmt.Errorf("invalid map size: %w", err)
	}
	if val.Elem().Len() == 0 {
		val.Elem().Set(reflect.MakeMapWithSize(mtype, size))
	}

	ktype := mtype.Key()
	vtype := mtype.Elem()
	for {
		nkey, err := readValue(buf)
		if err == io.EOF {
			break
		} else if err != nil {
			return err
		}
		nval, err := readValue(buf)
		if err == io.EOF {
			return errors.New("missing map value")
		} else if err != nil {
			return err
		}

		mkey := reflect.New(ktype)
		if err := Unmarshal(nkey, mkey.Interface()); err != nil {
			return fmt.Errorf("decoding map key: %w", err)
		}
		mval := reflect.New(vtype)
		if err := Unmarshal(nval, mval.Interface()); err != nil {
			return fmt.Errorf("decoding map value: %w", err)
		}
		val.Elem().SetMapIndex(mkey.Elem(), mval.Elem())
		size--
	}
	if size != 0 {
		return errors.New("invalid packed map")
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

		// Non-slice.
		if !fi.slice {
			if err := Unmarshal(data, fi.target); err != nil {
				return nil
			}
			continue
		}
		slc := reflect.ValueOf(fi.target)

		// Packed array.
		if fi.pack {
			if err := unmarshalSlice(data, slc); err != nil {
				return err
			}
			continue
		}

		// Inline slice element.
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
	return nil
}

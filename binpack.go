// Copyright (C) 2020 Michael J. Fromberger. All Rights Reserved.

// Package binpack implements a compact binary encoding format.
//
// A binpack message is a concatenated sequence of tag-value records. A tag is
// an unsigned integer value, a value is an array of bytes. The tags and values
// are opaque to the encoding; the caller must provide additional structure as
// needed.  For example, the application may encode type information in some
// low-order bits of the tag.
//
// Tags are encoded as 1, 2, or 4 bytes, having values up to 2^30-1.  Values
// are length-prefixed byte arrays up to 2^29-1 bytes in length.
//
// The enoding of a tag is as follows:
//
//   Byte 0 (index)
//   +---------------+
//   |0|   7 bits    | + 0 bytes  : values 0..127 (7 bits)
//   +---------------+
//   |1|0| 6 bits    | + 1 byte   : values 0..16383 (14 bits)
//   +---------------+
//   |1|1| 6 bits    | + 3 bytes  : values 0..1073741823 (30 bits)
//   +---------------+
//
// The first byte of the tag is called the index, and its high-order two bits
// determine the size of the tag in bytes (0_=1, 01=2, 11=4).
//
// The encoding of a value is as follows:
//
//   Byte 0 (index)
//   +---------------+
//   |0|   7 bits    | + 0 bytes         : length 1, value 0..127
//   +---------------+
//   |1|0| 6 bits    | + 0 bytes + data  : length 0..63
//   +---------------+
//   |1|1|0| 5 bits  | + 1 bytes + data  : length 0..8191
//   +---------------+
//   |1|1|1| 5 bits  | + 3 bytes + data  : length 0..536870911
//   +---------------+
//
// The first byte of the value is called the index, and its high-order three
// bits determine the size of the length prefix. Small single-byte values are
// encoded directly with a prefix of 0; otherwise the length is 1, 2, or 4
// bytes.
//
package binpack

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"strings"
)

// An Encoder encodes tag-value records to a buffer.  Call the Encode method to
// add values. The buffer can be recovered from the Data field.
type Encoder struct {
	Data *bytes.Buffer
}

// NewEncoder constructs an Encoder that writes data to buf. If buf == nil, a
// new empty buffer is allocated and can be retrieved from the Data field of
// the Encoder.
func NewEncoder(buf *bytes.Buffer) *Encoder {
	if buf == nil {
		buf = bytes.NewBuffer(nil)
	}
	return &Encoder{Data: buf}
}

// Encode appends a single tag-value pair to the output.
func (e *Encoder) Encode(tag int, value []byte) error {
	e.Data.Grow(tagSize(tag) + lengthSize(value) + len(value))
	err := writeTag(e.Data, tag)
	if err == nil {
		err = writeValue(e.Data, value)
	}
	return err
}

// tagSize returns the number of bytes needed to encode tag, or -1.
func tagSize(tag int) int {
	if tag < 128 {
		return 1
	} else if tag < (1 << 14) {
		return 2
	} else if tag < (1 << 30) {
		return 4
	}
	return -1
}

// writeTag appends the encoding of tag to w.
func writeTag(w io.Writer, tag int) (err error) {
	switch tagSize(tag) {
	case 1:
		_, err = w.Write([]byte{byte(tag)})
	case 2:
		_, err = w.Write([]byte{0x80 | byte(tag>>8), byte(tag & 0xff)})
	case 4:
		_, err = w.Write([]byte{
			0xC0 | byte(tag>>24), byte(tag >> 16), byte(tag >> 8), byte(tag),
		})
	default:
		return fmt.Errorf("tag too big (%d > %d)", tag, 1<<30-1)
	}
	return
}

// lengthSize returns the number of bytes to encode the length of value, or -1.
func lengthSize(value []byte) int {
	n := len(value)
	if n == 1 && value[0] < 128 {
		return 0
	} else if n < (1 << 6) {
		return 1
	} else if n < (1 << 13) {
		return 2
	} else if n < (1 << 29) {
		return 4
	}
	return -1
}

// writeValue writes the encoding of value to w.
func writeValue(w io.Writer, value []byte) error {
	n := len(value)
	var err error
	switch lengthSize(value) {
	case 0:
		_, err := w.Write([]byte{value[0]})
		return err
	case 1:
		_, err = w.Write([]byte{0x80 | byte(n)})
	case 2:
		_, err = w.Write([]byte{0xC0 | byte(n>>8), byte(n)})
	case 4:
		_, err = w.Write([]byte{0xE0 | byte(n>>24), byte(n >> 16), byte(n >> 8), byte(n)})
	default:
		return fmt.Errorf("value too big (%d bytes > %d)", len(value), 1<<29-1)
	}
	if err == nil {
		_, err = w.Write(value)
	}
	return err
}

// A Decoder decodes tag-value pairs from an io.Reader.
type Decoder struct {
	buf bufReader
}

// NewDecoder constructs a Decoder that reads records from r.
func NewDecoder(r io.Reader) *Decoder {
	switch t := r.(type) {
	case *bytes.Buffer, *bytes.Reader, *strings.Reader:
		return &Decoder{buf: t.(bufReader)}
	case *bufio.Reader:
		return &Decoder{buf: t}
	default:
		return &Decoder{buf: bufio.NewReader(r)}
	}
}

// Decode returns the next tag-value record from the reader.
// At the end of the input, it returns io.EOF.
func (d *Decoder) Decode() (int, []byte, error) {
	tag, err := readTag(d.buf)
	if err != nil {
		return 0, nil, err
	}
	value, err := readValue(d.buf)
	if err != nil {
		return tag, nil, err
	}
	return tag, value, err
}

type bufReader interface {
	io.Reader
	io.ByteReader
}

// readTag reads a tag from the current position of the decoder.
func readTag(buf bufReader) (int, error) {
	b, err := buf.ReadByte()
	if err != nil {
		return 0, err
	}
	switch v := b >> 6; v {
	case 0, 1:
		return int(b), nil
	case 2:
		c, err := buf.ReadByte()
		if err != nil {
			return 0, err
		}
		return int(b&0x3f)<<8 | int(c), nil
	default:
		z, err := readInt24(buf)
		if err != nil {
			return 0, err
		}
		return int(b&0x3f)<<24 | z, nil
	}
}

// readValue reads a value from the current position of the decoder.
func readValue(buf bufReader) ([]byte, error) {
	b, err := buf.ReadByte()
	if err != nil {
		return nil, err
	}
	var n int
	if v := b >> 5; v < 4 {
		// index with 1-byte value; no additional data bytes
		return []byte{b}, nil
	} else if v < 6 {
		// index + data
		n = int(b & 0x3f)
	} else if v == 6 {
		// index + 2 + data
		c, err := buf.ReadByte()
		if err != nil {
			return nil, err
		}
		n = int(b&0x1f)<<8 | int(c)
	} else {
		// index + 3 + data
		n, err = readInt24(buf)
		if err != nil {
			return nil, err
		}
	}

	// Now n is the number of data bytes we need to read.
	data := make([]byte, n)
	if _, err := io.ReadFull(buf, data); err != nil {
		return nil, err
	}
	return data, nil
}

// readInt24 reads three bytes from the input and decodes the value as an
// unsigned integer in big-endian order.
func readInt24(buf bufReader) (int, error) {
	var data [3]byte
	if _, err := io.ReadFull(buf, data[:]); err != nil {
		return 0, err
	}
	return int(data[0])<<16 | int(data[1])<<8 | int(data[2]), nil
}

// PackUint64 encodes z as a slice in big-endian order, omitting leading zeroes.
// The encoding of 0 is a slice of length 1.
func PackUint64(z uint64) []byte {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], z)
	for i, b := range buf {
		if b != 0 {
			return buf[i:]
		}
	}
	return buf[:1]
}

// UnpackUint64 decodes z from a big-endian slice.
func UnpackUint64(data []byte) uint64 {
	var z uint64
	for _, b := range data {
		z = (z << 8) | uint64(b)
	}
	return z
}

// PackInt64 encodes z as a slice in big-endian order with zigzag encoding,
// omitting leading zeroes. The encoding of 0 is a slice of length 1.
//
// Zigzag encoding represents a signed value as the bitwise complement of its
// 2s complement value, with its sign in the least-significant bit.
func PackInt64(z int64) []byte {
	u := uint64(z<<1) ^ uint64(z>>63)
	return PackUint64(u)
}

// UnpackInt64 decodes z from a big-endian slice with zigzag encoding.
func UnpackInt64(data []byte) int64 {
	z := UnpackUint64(data)
	mask := math.MaxUint64 + (1 - z&1)
	return int64(mask ^ z>>1)
}

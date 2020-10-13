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
	"fmt"
	"io"
)

// A Buffer wraps encoder that writes to a byte buffer. The caller can recover
// the byte buffer from the Data field.
type Buffer struct {
	Data *bytes.Buffer
	*Encoder
}

// NewBuffer constructs a new Buffer with the given data as its initial contents.
// A Buffer wraps an Encoder that writes to a byte buffer.
func NewBuffer(buf []byte) *Buffer {
	data := bytes.NewBuffer(nil)
	return &Buffer{Data: data, Encoder: NewEncoder(data)}
}

// An Encoder encodes tag-value records to an io.Writer.
// Call the Encode method to add values. You must call Flush when finished to
// ensure all buffered output is written to the underlying writer.
type Encoder struct {
	buf *bufio.Writer
}

// NewEncoder constructs an Encoder that writes data to w.
func NewEncoder(w io.Writer) *Encoder {
	return &Encoder{buf: bufio.NewWriter(w)}
}

// Encode appends a single tag-value pair to the output.
func (e *Encoder) Encode(tag int, value []byte) error {
	err := writeTag(e.buf, tag)
	if err == nil {
		err = writeValue(e.buf, value)
	}
	return err
}

// Flush flushes buffered data to the underlying writer.
func (e *Encoder) Flush() error { return e.buf.Flush() }

// writeTag appends the encoding of tag to w.
func writeTag(w *bufio.Writer, tag int) (err error) {
	if tag < 128 {
		return w.WriteByte(byte(tag))
	} else if tag < (1 << 14) {
		_, err = w.Write([]byte{0x80 | byte(tag>>8), byte(tag & 0xff)})
	} else if tag < (1 << 30) {
		_, err = w.Write([]byte{
			0xC0 | byte(tag>>24), byte(tag >> 16), byte(tag >> 8), byte(tag),
		})
	} else {
		return fmt.Errorf("tag too big (%d > %d)", tag, 1<<30-1)
	}
	return
}

// writeValue writes the encoding of value to w.
func writeValue(w *bufio.Writer, value []byte) error {
	n := len(value)
	if n == 1 && value[0] < 128 {
		return w.WriteByte(value[0])
	}
	var err error
	if n < (1 << 6) {
		err = w.WriteByte(0x80 | byte(n))
		if err == nil {
			_, err = w.Write(value)
		}
	} else if n < (1 << 13) {
		_, err = w.Write([]byte{0xC0 | byte(n>>8), byte(n)})
		if err == nil {
			_, err = w.Write(value)
		}
	} else if n < (1 << 29) {
		_, err = w.Write([]byte{0xE0 | byte(n>>24), byte(n >> 16), byte(n >> 8), byte(n)})
		if err == nil {
			_, err = w.Write(value)
		}
	} else {
		return fmt.Errorf("value too big (%d bytes > %d)", len(value), 1<<29-1)
	}
	return err
}

// A Decoder decodes tag-value pairs from an io.Reader.
type Decoder struct {
	buf *bufio.Reader
}

// NewDecoder constructs a Decoder that reads records from r.
func NewDecoder(r io.Reader) *Decoder {
	return &Decoder{buf: bufio.NewReader(r)}
}

// Decode returns the next tag-value record from the reader.
// At the end of the input, it returns io.EOF.
func (d *Decoder) Decode() (int, []byte, error) {
	tag, err := d.readTag()
	if err != nil {
		return 0, nil, err
	}
	value, err := d.readValue()
	if err != nil {
		return tag, nil, err
	}
	return tag, value, err
}

// readTag reads a tag from the current position of the decoder.
func (d *Decoder) readTag() (int, error) {
	b, err := d.buf.ReadByte()
	if err != nil {
		return 0, err
	}
	switch v := b >> 6; v {
	case 0, 1:
		return int(b), nil
	case 2:
		c, err := d.buf.ReadByte()
		if err != nil {
			return 0, err
		}
		return int(b&0x3f)<<8 | int(c), nil
	default:
		z, err := d.readInt24()
		if err != nil {
			return 0, err
		}
		return int(b&0x3f)<<24 | z, nil
	}
}

// readValue reads a value from the current position of the decoder.
func (d *Decoder) readValue() ([]byte, error) {
	b, err := d.buf.ReadByte()
	if err != nil {
		return nil, err
	}
	var n int
	switch v := b >> 5; v {
	case 0, 1, 2, 3: // literal single-byte value
		return []byte{b}, nil
	case 4, 5: // length in index byte
		n = int(b & 0x3f)
	case 6: // index + 2
		c, err := d.buf.ReadByte()
		if err != nil {
			return nil, err
		}
		n = int(b&0x1f)<<8 | int(c)
	case 7: // 4 bytes after index
		n, err = d.readInt24()
		if err != nil {
			return nil, err
		}
	}

	// Now n is the number of data bytes we need to read.
	data := make([]byte, n)
	if _, err := io.ReadFull(d.buf, data); err != nil {
		return nil, err
	}
	return data, nil
}

// readInt reads three bytes from the input and decodes the value as an
// unsigned integer in big-endian order.
func (d *Decoder) readInt24() (int, error) {
	var buf [3]byte
	if _, err := io.ReadFull(d.buf, buf[:]); err != nil {
		return 0, err
	}
	return int(buf[0])<<16 | int(buf[1])<<8 | int(buf[2]), nil
}

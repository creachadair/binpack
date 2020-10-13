// Copyright (C) 2020 Michael J. Fromberger. All Rights Reserved.

package binpack_test

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/creachadair/binpack"
)

func TestDecodeEmpty(t *testing.T) {
	d := binpack.NewDecoder(strings.NewReader(""))
	tag, value, err := d.Decode()
	if err != io.EOF {
		t.Errorf("Decode: got tag=%d, value=%q, err=%v; want EOF", tag, value, err)
	}
}

func TestEncodeSeveral(t *testing.T) {
	input := []string{"cogwheel", "kiss", "failure", "x"}

	var buf bytes.Buffer
	e := binpack.NewEncoder(&buf)

	// Encode the inputs using their lengths as tags.
	for _, s := range input {
		if err := e.Encode(len(s), []byte(s)); err != nil {
			t.Fatalf("Encode %q failed: %v", s, err)
		}
	}
	if err := e.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// Verify the binary format is right (hand-constructed).
	const want = "\x08\x88cogwheel\x04\x84kiss\x07\x87failure\x01x"
	if got := buf.String(); got != want {
		t.Errorf("Encoded result: got %q, want %q", got, want)
	}

	// Verify we can get the original values back out, in order.
	d := binpack.NewDecoder(&buf)
	for i := 0; ; i++ {
		tag, value, err := d.Decode()
		if err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("Decode: unexpected error: %v", err)
		}
		if tag != len(value) {
			t.Errorf("Decode: tag=%d, want %d", tag, len(value))
		}
		if got := string(value); got != input[i] {
			t.Errorf("Decode: value=%q, want %q", got, input[i])
		}
	}
}

func TestRoundTrip(t *testing.T) {
	tests := []struct {
		tag   int
		value string
		want  string
	}{
		// Single-byte values.
		{0, "\x00", "\x00\x00"},                 // value fits in 7 bits
		{0, "\x10", "\x00\x10"},                 // "
		{1, "\x8f", "\x01\x81\x8f"},             // value requires > 7 bits
		{129, "\x00", "\x80\x81\x00"},           // tag requires > 7 bits
		{18000, "\x7f", "\xC0\x00\x46\x50\x7f"}, // tag requires > 14 bits

		// Empty value.
		{0, "", "\x00\x80"},

		// Non-empty values.
		{0, "foo", "\x00\x83foo"},
		{15, "crazytrain", "\x0f\x8acrazytrain"},
		{72, "this string is seventy-five bytes in length and that may surprise you maybe",
			"\x48\xc0\x4bthis string is seventy-five bytes in length and that may surprise you maybe",
		}, // value exceeds 1-byte length marker
		{170, strings.Repeat("a", 9000),
			"\x80\xaa\xE0\x00\x23\x28" + strings.Repeat("a", 9000),
		}, // value exceeds 2-byte length marker
	}

	for _, test := range tests {
		var buf bytes.Buffer
		e := binpack.NewEncoder(&buf)
		if err := e.Encode(test.tag, []byte(test.value)); err != nil {
			t.Errorf("Encode(%d, %q): unexpected error: %v", test.tag, capLen(test.value), err)
			continue
		} else if err := e.Flush(); err != nil {
			t.Errorf("Flush: unexpected error: %v", err)
			continue
		}
		got := buf.String()
		if got != test.want {
			t.Errorf("Encode(%d, %q): got %q, want %q",
				test.tag, capLen(test.value), capLen(got), capLen(test.want))
		}

		// Ensure we can round-trip the value.
		d := binpack.NewDecoder(&buf)
		tag, value, err := d.Decode()
		if err != nil {
			t.Errorf("Decode failed: %v", err)
		}
		if tag != test.tag {
			t.Errorf("Decode tag: got %d, want %d", tag, test.tag)
		}
		if got := string(value); got != test.value {
			t.Errorf("Decode value: got %q, want %q", capLen(got), capLen(test.value))
		}

		// No extra garbage allowed.
		if tag, value, err := d.Decode(); err != io.EOF {
			t.Errorf("Decode: got %v, %q, %v; want EOF", tag, capLen(string(value)), err)
		}
	}
}

func capLen(s string) string {
	const maxLen = 30
	if len(s) > maxLen {
		return s[:maxLen-3] + "..."
	}
	return s
}

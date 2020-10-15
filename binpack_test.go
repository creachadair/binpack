// Copyright (C) 2020 Michael J. Fromberger. All Rights Reserved.

package binpack_test

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/creachadair/binpack"
	"github.com/google/go-cmp/cmp"
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

	e := binpack.NewBuffer(nil)

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
	if got := e.Data.String(); got != want {
		t.Errorf("Encoded result: got %q, want %q", got, want)
	}

	// Verify we can get the original values back out, in order.
	d := binpack.NewDecoder(e.Data)
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
		e := binpack.NewBuffer(nil)
		if err := e.Encode(test.tag, []byte(test.value)); err != nil {
			t.Errorf("Encode(%d, %q): unexpected error: %v", test.tag, capLen(test.value), err)
			continue
		} else if err := e.Flush(); err != nil {
			t.Errorf("Flush: unexpected error: %v", err)
			continue
		}
		got := e.Data.String()
		if got != test.want {
			t.Errorf("Encode(%d, %q): got %q, want %q",
				test.tag, capLen(test.value), capLen(got), capLen(test.want))
		}

		// Ensure we can round-trip the value.
		d := binpack.NewDecoder(e.Data)
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

func TestMarshalRoundTrip(t *testing.T) {
	type tag struct {
		Key   string `binpack:"tag=1"`
		Value int    `binpack:"tag=2"`
	}
	type thing struct {
		Name   string  `binpack:"tag=10"`
		Tags   []*tag  `binpack:"tag=30"`
		Slogan *tag    `binpack:"tag=20"`
		Hot    bool    `binpack:"tag=70"`
		Counts []int   `binpack:"tag=40,pack"`
		Zero   float64 `binpack:"tag=15"`

		Set map[string]struct{} `binpack:"tag=60"`
	}

	in := &thing{
		Name: "Harcourt Fenton Mudd",
		Tags: []*tag{
			{Key: "dalmatians", Value: 101},
			{Key: "skeeziness", Value: 9001},
		},
		Slogan: &tag{Key: "orange man bad", Value: -15},
		Hot:    true,
		Counts: []int{17, 69, 1814, 1918, 1936},
		Set: map[string]struct{}{
			"horse": {},
			"cake":  {},
		},
	}

	bits, err := binpack.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	t.Logf("Marshal OK, output is %d bytes", len(bits))
	t.Logf("Output: %q", string(bits))
	dec := binpack.NewDecoder(bytes.NewReader(bits))
	for i := 0; ; i++ {
		tag, data, err := dec.Decode()
		if err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("Decode failed: %v", err)
		}
		t.Logf("Record %d: tag=%d data=%q", i+1, tag, string(data))
	}

	out := new(thing)
	if err := binpack.Unmarshal(bits, out); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if diff := cmp.Diff(in, out); diff != "" {
		t.Errorf("Unmarshal output differs (-want, +got):\n%s", diff)
	}
}

func TestUnmarshalPacked(t *testing.T) {
	// This input mixes packed and unpacked values. Verify that the decoding
	// them correctly combines the two forms.
	//
	//              /-- unpacked values--\ /-- packed values -\/------ map entry ------\
	//             [  1   ][  2   ][  3   ][         4   5   6][     "xoxo":true       ]
	const input = "\x0A\x02\x0A\x04\x0A\x06\x0A\x83\x08\x0A\x0C\x14\x87\x86\x84xoxo\x01" +
		"\x14\x8e\x85\x83tla\x00\x87\x85heart\x00"
	// packed:  [ "tla":false ][ "heart":false  ]

	type test struct {
		V []int           `binpack:"tag=10,pack"`
		M map[string]bool `binpack:"tag=20,pack"`
	}

	var got test
	if err := binpack.Unmarshal([]byte(input), &got); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	want := test{
		V: []int{1, 2, 3, 4, 5, 6},
		M: map[string]bool{
			"xoxo":  true,
			"heart": false,
			"tla":   false,
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Unmarshal output (-want, +got):\n%s", diff)
	}
}

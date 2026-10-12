//go:build linux

package main

import (
	"slices"
	"strings"
	"testing"
)

// feed runs chunks through the framer and collects everything it produced.
func feed(f *framer, chunks ...string) (tokens []string, discards []discard) {
	for _, chunk := range chunks {
		found, dropped := f.write([]byte(chunk))
		tokens, discards = append(tokens, found...), append(discards, dropped...)
	}
	return tokens, discards
}

func TestFramerTerminatorSelection(t *testing.T) {
	cases := []struct {
		name       string
		terminator terminator
		input      string
		tokens     []string
	}{
		{"any ends on CR", terminatorAny, "pmt_A\r", []string{"pmt_A"}},
		{"any ends on LF", terminatorAny, "pmt_A\n", []string{"pmt_A"}},
		{"any treats CRLF as one end", terminatorAny, "pmt_A\r\n", []string{"pmt_A"}},
		{"cr accepts CR", terminatorCR, "pmt_A\r", []string{"pmt_A"}},
		{"cr does not end on LF", terminatorCR, "pmt_A\n", nil},
		{"lf accepts LF", terminatorLF, "pmt_A\n", []string{"pmt_A"}},
		{"lf does not end on CR", terminatorLF, "pmt_A\r", nil},
		{"none does not end on either", terminatorNone, "pmt_A\r\n", nil},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			f := &framer{terminator: item.terminator}
			tokens, discards := feed(f, item.input)
			if !slices.Equal(tokens, item.tokens) {
				t.Fatalf("tokens = %q, want %q", tokens, item.tokens)
			}
			if len(discards) != 0 {
				t.Fatalf("unexpected discards %v", discards)
			}
		})
	}
}

func TestFramerTerminatorNamesMatchConfigurationValues(t *testing.T) {
	for name, value := range scannerTerminators {
		if value.String() != name {
			t.Fatalf("terminator %s names itself %q", name, value.String())
		}
	}
}

// A scanner configured without a suffix never sends a terminator, so silence is
// the only end-of-frame signal available.
func TestFramerIdleEndsFrameWithoutTerminator(t *testing.T) {
	f := &framer{terminator: terminatorAny}
	if tokens, _ := feed(f, "pmt_A"); len(tokens) != 0 {
		t.Fatalf("frame ended without a terminator or an idle period: %q", tokens)
	}
	tokens, discards := f.idle()
	if !slices.Equal(tokens, []string{"pmt_A"}) || len(discards) != 0 {
		t.Fatalf("idle produced tokens %q discards %v", tokens, discards)
	}
	// A second idle period with nothing in between must stay silent.
	if tokens, discards := f.idle(); len(tokens) != 0 || len(discards) != 0 {
		t.Fatalf("empty idle produced tokens %q discards %v", tokens, discards)
	}
}

func TestFramerCompletesFrameSplitAcrossReads(t *testing.T) {
	f := &framer{terminator: terminatorAny}
	for _, chunk := range []string{"pmt_", "ABCD"} {
		if tokens, discards := f.write([]byte(chunk)); len(tokens) != 0 || len(discards) != 0 {
			t.Fatalf("incomplete frame produced tokens %q discards %v", tokens, discards)
		}
	}
	tokens, discards := f.write([]byte("\r"))
	if !slices.Equal(tokens, []string{"pmt_ABCD"}) || len(discards) != 0 {
		t.Fatalf("tokens = %q discards = %v", tokens, discards)
	}
}

func TestFramerCompletesEveryFrameInOneRead(t *testing.T) {
	f := &framer{terminator: terminatorAny}
	tokens, discards := feed(f, "pmt_A\rpmt_B\npmt_C\r\n")
	if !slices.Equal(tokens, []string{"pmt_A", "pmt_B", "pmt_C"}) || len(discards) != 0 {
		t.Fatalf("tokens = %q discards = %v", tokens, discards)
	}
}

// Padding is stripped from the ends before the middle is inspected, which is what
// keeps a scanner configured to pad short codes working.
func TestFramerStripsPaddingBeforeInspectingFrame(t *testing.T) {
	for _, input := range []string{"pmt_ABCD\x00\x00\x00\x00\r", "\x00pmt_ABCD\x00\r", "pmt_ABCD   \r", "\tpmt_ABCD\r\n"} {
		f := &framer{terminator: terminatorAny}
		tokens, discards := feed(f, input)
		if !slices.Equal(tokens, []string{"pmt_ABCD"}) || len(discards) != 0 {
			t.Fatalf("%q produced tokens %q discards %v", input, tokens, discards)
		}
	}
}

// Padding is only stripped from the ends: an unprintable byte in the middle is
// what a baud rate mismatch looks like.
func TestFramerDiscardsUnprintableFrame(t *testing.T) {
	for _, input := range []string{"pmt_\xff\xfe\r", "pmt_A\x01B\r", "pmt_A\x00B\r", "pmt_A\x7fB\r"} {
		f := &framer{terminator: terminatorAny}
		tokens, discards := feed(f, input)
		if len(tokens) != 0 {
			t.Fatalf("%q was submitted as %q", input, tokens)
		}
		if len(discards) != 1 || discards[0].reason != "unprintable" {
			t.Fatalf("%q produced discards %v", input, discards)
		}
	}
}

// An oversized frame is discarded whole rather than truncated, and the framer
// keeps working on the next frame.
func TestFramerDiscardsOversizedFrameWhole(t *testing.T) {
	oversized := "pmt_" + strings.Repeat("A", 200)
	f := &framer{terminator: terminatorAny}
	tokens, discards := feed(f, oversized+"\r", "pmt_OK\r")
	if !slices.Equal(tokens, []string{"pmt_OK"}) {
		t.Fatalf("tokens = %q", tokens)
	}
	if len(discards) != 1 || discards[0].reason != "oversized" {
		t.Fatalf("discards = %v", discards)
	}
	if discards[0].bytes != len(oversized) {
		t.Fatalf("oversized frame reported %d bytes, want %d", discards[0].bytes, len(oversized))
	}
}

// A frame of exactly the accepted length still works.
func TestFramerAcceptsFrameAtLengthLimit(t *testing.T) {
	value := strings.Repeat("A", frameLimit)
	f := &framer{terminator: terminatorAny}
	tokens, discards := feed(f, value+"\r")
	if !slices.Equal(tokens, []string{value}) || len(discards) != 0 {
		t.Fatalf("tokens = %q discards = %v", tokens, discards)
	}
}

// A scanner sending both CR and LF leaves an empty frame between them; that is
// normal traffic and must not be reported.
func TestFramerDropsEmptyFramesSilently(t *testing.T) {
	f := &framer{terminator: terminatorAny}
	tokens, discards := feed(f, "\r\n  \t\r\n")
	if len(tokens) != 0 || len(discards) != 0 {
		t.Fatalf("tokens = %q discards = %v", tokens, discards)
	}
}

//go:build linux

package main

import "bytes"

// frameLimit caps a frame at the length the server accepts for a payment token.
// A longer frame is discarded whole rather than truncated: a truncated value
// still looks like a plausible token.
const frameLimit = 128

// terminator selects which characters end a frame. The suffix belongs to the
// scanner's own configuration, not to the agent, so this has to accept either
// character, one of them, or no terminator at all.
type terminator int

const (
	terminatorAny terminator = iota // CR or LF
	terminatorCR
	terminatorLF
	terminatorNone
)

func (t terminator) ends(value byte) bool {
	switch t {
	case terminatorAny:
		return value == '\r' || value == '\n'
	case terminatorCR:
		return value == '\r'
	case terminatorLF:
		return value == '\n'
	}
	return false
}

func (t terminator) String() string {
	switch t {
	case terminatorAny:
		return "any"
	case terminatorCR:
		return "cr"
	case terminatorLF:
		return "lf"
	}
	return "none"
}

// discard is a frame the framer refused to submit. A stream of them is what a
// baud rate mismatch looks like, so the size and reason are logged.
type discard struct {
	bytes  int
	reason string
}

// framer turns a serial byte stream into barcode tokens. It holds no device
// state, so its behavior is unit tested without hardware attached.
type framer struct {
	terminator terminator
	buffer     []byte
	overflow   bool
	// overlong counts the bytes of an oversized frame past the buffer cap, so a
	// discarded frame reports its real length instead of the cap.
	overlong int
}

// write feeds one chunk read from the port and returns the frames it completed.
func (f *framer) write(chunk []byte) (tokens []string, discards []discard) {
	for _, value := range chunk {
		if f.terminator.ends(value) {
			f.complete(&tokens, &discards)
			continue
		}
		if f.overflow {
			f.overlong++ // Keep counting so a discarded frame reports its real length.
			continue
		}
		if len(f.buffer) >= frameLimit {
			f.overflow = true
			f.overlong++
			continue
		}
		f.buffer = append(f.buffer, value)
	}
	return tokens, discards
}

// idle completes the frame in progress once the port has been silent for the
// configured window. A scanner configured without a suffix never sends a
// terminator, so silence is the only end-of-frame signal available.
func (f *framer) idle() (tokens []string, discards []discard) {
	f.complete(&tokens, &discards)
	return tokens, discards
}

// complete closes the frame in progress. Leading and trailing NUL, CR, LF, space
// and TAB are stripped first, and only then is the middle inspected for
// unprintable bytes. Stripping first is what keeps a scanner configured to pad
// short codes with NUL working; inspecting the middle still rejects the mojibake
// a baud rate mismatch produces.
func (f *framer) complete(tokens *[]string, discards *[]discard) {
	size, overflow, overlong := len(f.buffer), f.overflow, f.overlong
	token := string(bytes.Trim(f.buffer, "\x00\r\n \t")) // Copies before the buffer is reused.
	f.buffer, f.overflow, f.overlong = f.buffer[:0], false, 0
	if overflow {
		*discards = append(*discards, discard{bytes: size + overlong, reason: "oversized"})
		return
	}
	if token == "" {
		return // A scanner sending both CR and LF leaves an empty frame between them.
	}
	if !printable(token) {
		*discards = append(*discards, discard{bytes: size, reason: "unprintable"})
		return
	}
	*tokens = append(*tokens, token)
}

func printable(value string) bool {
	for index := 0; index < len(value); index++ {
		if value[index] < 0x20 || value[index] > 0x7e {
			return false
		}
	}
	return true
}

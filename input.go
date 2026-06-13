package main

import (
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

type keyKind int

const (
	keyUnknown keyKind = iota
	keyRune            // a printable rune, in key.r
	keyEnter
	keyBackspace
	keyTab
	keyEsc
	keyUp
	keyDown
	keyLeft
	keyRight
	keyHome
	keyEnd
	keyPageUp
	keyPageDown
	keyInsert
	keyDelete
	keyCtrlC
	keyCtrlS
	keyCtrlX
	keyCtrlU
)

type key struct {
	kind keyKind
	r    rune // valid when kind == keyRune
	ctrl bool // a Ctrl modifier rode along with a navigation key
}

// reader decodes terminal input into keys. It buffers the bytes from each Read
// so an escape sequence delivered in one chunk is parsed whole.
type reader struct {
	in   *os.File
	raw  [256]byte
	data []byte
}

func newReader() *reader { return &reader{in: os.Stdin} }

func (rd *reader) readByte() (byte, bool) {
	if len(rd.data) == 0 {
		n, err := rd.in.Read(rd.raw[:])
		if err != nil || n == 0 {
			return 0, false
		}
		rd.data = rd.raw[:n]
	}
	b := rd.data[0]
	rd.data = rd.data[1:]
	return b, true
}

// escTimeout is how long readKey waits after a lone ESC for the rest of an
// escape sequence before deciding the ESC was the Escape key itself. A terminal
// sends an arrow's bytes back-to-back, so a gap this long means a real Escape.
const escTimeout = 50 * time.Millisecond

// waitByte reports whether a byte is available within d, pulling it into the
// buffer if so. It is how readKey distinguishes a bare Escape from the lead byte
// of an arrow sequence that arrived in a separate read.
func (rd *reader) waitByte(d time.Duration) bool {
	if len(rd.data) > 0 {
		return true
	}
	fds := []unix.PollFd{{Fd: int32(rd.in.Fd()), Events: unix.POLLIN}}
	n, err := unix.Poll(fds, int(d/time.Millisecond))
	if err != nil || n == 0 {
		return false
	}
	m, err := rd.in.Read(rd.raw[:])
	if err != nil || m == 0 {
		return false
	}
	rd.data = rd.raw[:m]
	return true
}

// readKey returns the next decoded key. ok is false at end of input.
func (rd *reader) readKey() (key, bool) {
	b, ok := rd.readByte()
	if !ok {
		return key{}, false
	}
	switch {
	case b == 0x1b: // ESC: sequence lead-in, or the bare Escape key
		if rd.waitByte(escTimeout) {
			if rd.data[0] == '[' {
				rd.readByte()
				return rd.readCSI(), true
			}
			if rd.data[0] == 'O' { // application-cursor arrows
				rd.readByte()
				return rd.readSS3(), true
			}
		}
		return key{kind: keyEsc}, true
	case b == '\r', b == '\n':
		return key{kind: keyEnter}, true
	case b == 0x7f, b == 0x08:
		return key{kind: keyBackspace}, true
	case b == '\t':
		return key{kind: keyTab}, true
	case b == 0x03:
		return key{kind: keyCtrlC}, true
	case b == 0x13:
		return key{kind: keyCtrlS}, true
	case b == 0x18:
		return key{kind: keyCtrlX}, true
	case b == 0x15:
		return key{kind: keyCtrlU}, true
	case b < 0x20:
		return key{kind: keyUnknown}, true
	case b < 0x80:
		return key{kind: keyRune, r: rune(b)}, true
	default:
		return rd.readUTF8(b), true
	}
}

// readCSI parses the body of an ESC [ … sequence (the leading ESC [ already
// consumed): parameter bytes terminated by a final byte in 0x40–0x7e.
func (rd *reader) readCSI() key {
	var params []byte
	for {
		b, ok := rd.readByte()
		if !ok {
			return key{kind: keyEsc}
		}
		if b >= 0x40 && b <= 0x7e {
			return classifyCSI(params, b)
		}
		params = append(params, b)
	}
}

// classifyCSI maps a parsed CSI sequence to a key. The modifier parameter is
// 1 + a bitmask (Shift 1, Alt 2, Ctrl 4), so a "5" second parameter means Ctrl.
func classifyCSI(params []byte, final byte) key {
	nums := parseParams(params)
	ctrl := false
	if len(nums) >= 2 {
		ctrl = (nums[1]-1)&4 != 0
	}
	switch final {
	case 'A':
		return key{kind: keyUp, ctrl: ctrl}
	case 'B':
		return key{kind: keyDown, ctrl: ctrl}
	case 'C':
		return key{kind: keyRight, ctrl: ctrl}
	case 'D':
		return key{kind: keyLeft, ctrl: ctrl}
	case 'H':
		return key{kind: keyHome, ctrl: ctrl}
	case 'F':
		return key{kind: keyEnd, ctrl: ctrl}
	case '~':
		n := 0
		if len(nums) >= 1 {
			n = nums[0]
		}
		switch n {
		case 2:
			return key{kind: keyInsert, ctrl: ctrl}
		case 3:
			return key{kind: keyDelete, ctrl: ctrl}
		case 5:
			return key{kind: keyPageUp, ctrl: ctrl}
		case 6:
			return key{kind: keyPageDown, ctrl: ctrl}
		}
	}
	return key{kind: keyUnknown}
}

// readSS3 parses ESC O <letter>, the application-cursor-mode arrow form some
// terminals send instead of ESC [ <letter>.
func (rd *reader) readSS3() key {
	b, ok := rd.readByte()
	if !ok {
		return key{kind: keyEsc}
	}
	switch b {
	case 'A':
		return key{kind: keyUp}
	case 'B':
		return key{kind: keyDown}
	case 'C':
		return key{kind: keyRight}
	case 'D':
		return key{kind: keyLeft}
	case 'H':
		return key{kind: keyHome}
	case 'F':
		return key{kind: keyEnd}
	}
	return key{kind: keyUnknown}
}

func parseParams(p []byte) []int {
	if len(p) == 0 {
		return nil
	}
	parts := strings.Split(string(p), ";")
	nums := make([]int, len(parts))
	for i, s := range parts {
		nums[i], _ = strconv.Atoi(s)
	}
	return nums
}

// readUTF8 gathers the continuation bytes of a multi-byte rune whose lead byte
// has already been read.
func (rd *reader) readUTF8(lead byte) key {
	var n int
	switch {
	case lead&0xe0 == 0xc0:
		n = 2
	case lead&0xf0 == 0xe0:
		n = 3
	case lead&0xf8 == 0xf0:
		n = 4
	default:
		return key{kind: keyUnknown} // stray continuation byte
	}
	bytes := make([]byte, 1, 4)
	bytes[0] = lead
	for len(bytes) < n {
		b, ok := rd.readByte()
		if !ok {
			break
		}
		bytes = append(bytes, b)
	}
	r, _ := utf8.DecodeRune(bytes)
	if r == utf8.RuneError {
		return key{kind: keyUnknown}
	}
	return key{kind: keyRune, r: r}
}

// readLine reads a plain line for a prompt (e.g. the filename ask), echoing as
// it goes. ok is false when cancelled with Ctrl+C or at end of input.
func readLine(rd *reader, prompt string) (string, bool) {
	os.Stdout.WriteString(prompt)
	var line []rune
	for {
		k, ok := rd.readKey()
		if !ok {
			os.Stdout.WriteString("\r\n")
			return "", false
		}
		switch k.kind {
		case keyCtrlC:
			os.Stdout.WriteString("\r\n")
			return "", false
		case keyEnter:
			os.Stdout.WriteString("\r\n")
			return string(line), true
		case keyBackspace:
			if len(line) > 0 {
				line = line[:len(line)-1]
				os.Stdout.WriteString("\b \b")
			}
		case keyRune:
			line = append(line, k.r)
			os.Stdout.WriteString(string(k.r))
		}
	}
}

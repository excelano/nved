//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// enableVirtualTerminal switches on the Windows console's ANSI (virtual
// terminal) processing. A Windows console prints nved's escape sequences as
// literal text until VT processing is enabled, one handle at a time: output VT
// so the cursor-control and faint-SGR codes nved emits are honored, and input
// VT so arrow and editing keys arrive as the same ESC[ sequences the Unix path
// already decodes. term.MakeRaw put the input handle into raw mode but does not
// touch the output handle's VT bit, so both are set here — right after raw mode
// and before the first escape is written. A handle that isn't a console (output
// redirected to a file or pipe) is left alone.
func enableVirtualTerminal() {
	enableConsoleFlag(os.Stdout.Fd(), windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING)
	enableConsoleFlag(os.Stdin.Fd(), windows.ENABLE_VIRTUAL_TERMINAL_INPUT)
}

func enableConsoleFlag(fd uintptr, flag uint32) {
	h := windows.Handle(fd)
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return // not a console — nothing to enable
	}
	_ = windows.SetConsoleMode(h, mode|flag)
}

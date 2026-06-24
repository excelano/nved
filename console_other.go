//go:build !windows

package main

// enableVirtualTerminal is a no-op on Unix: terminals interpret the ANSI escape
// sequences nved emits natively, so the hand-emitted control codes need no
// console setup. The Windows build supplies the real implementation.
func enableVirtualTerminal() {}

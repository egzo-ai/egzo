// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package session

// Typed says whether input from a client holds anything a person typed. A terminal also sends input on its own: focus events (the
// program turned on mode 1004, which the holder replays to every client), mouse reports, and its answers to the program's queries (cursor
// position, device attributes, modes, colors, capabilities). Those go to the program like the rest, but none of them is a person at the
// keyboard, so none of them holds back what is typed for the agent (Delivery.HumanQuiet).
func Typed(input []byte) bool {
	for i := 0; i < len(input); {
		if input[i] != 0x1b || i+1 >= len(input) {
			return true // a key, or a lone Escape
		}
		var n int
		var human bool
		switch input[i+1] {
		case '[':
			n, human = csi(input[i:])
		case ']', 'P', '_', '^', 'X': // OSC, DCS, APC, PM, SOS: only ever answers from the terminal
			n = stringSequence(input[i:])
		default:
			return true // Alt and a key
		}
		if human || n == 0 {
			return true
		}
		i += n
	}
	return false
}

// csi measures the control sequence at the start of input and says whether a person typed it (arrows, function keys, a paste, kitty
// keyboard keys). Zero is an incomplete sequence.
func csi(input []byte) (int, bool) {
	i := 2
	start := i
	for i < len(input) && input[i] >= 0x30 && input[i] <= 0x3f {
		i++
	}
	params := string(input[start:i])
	for i < len(input) && input[i] >= 0x20 && input[i] <= 0x2f {
		i++
	}
	if i >= len(input) || input[i] < 0x40 || input[i] > 0x7e {
		return 0, true
	}
	final, n := input[i], i+1
	private := params != "" && (params[0] == '?' || params[0] == '>' || params[0] == '<' || params[0] == '=')
	switch {
	case (final == 'I' || final == 'O') && params == "": // focus in, out
		return n, false
	case final == 'M' && params == "": // an X10 mouse report: three bytes follow
		if n+3 > len(input) {
			return 0, true
		}
		return n + 3, false
	case (final == 'M' || final == 'm') && params != "" && params[0] == '<': // an SGR mouse report
		return n, false
	case final == 'R', final == 'n', final == 'y', final == 't': // cursor position, status, mode and window reports
		return n, false
	case private && (final == 'c' || final == 'u'): // device attributes; the kitty keyboard flags in use
		return n, false
	}
	return n, true
}

// stringSequence measures an OSC, DCS, APC, PM or SOS string, ended by BEL or ST (ESC \). Zero is an incomplete one.
func stringSequence(input []byte) int {
	for i := 2; i < len(input); i++ {
		if input[i] == 0x07 {
			return i + 1
		}
		if input[i] == 0x1b && i+1 < len(input) && input[i+1] == '\\' {
			return i + 2
		}
	}
	return 0
}

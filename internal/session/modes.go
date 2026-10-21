// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package session

import (
	"strconv"
	"strings"
)

// replayedModes are the DEC private modes that decide how a terminal turns input into bytes: cursor keys, mouse reporting,
// focus events and bracketed paste. A TUI sets them once, when it starts, and a terminal that attaches later has never seen
// that, so it would select text itself instead of sending the mouse to the program. The screen modes (alternate screen,
// cursor visibility, synchronized output) are left out: the repaint a client gets draws those again.
var replayedModes = map[int]bool{1: true, 9: true, 1000: true, 1001: true, 1002: true, 1003: true, 1004: true, 1005: true, 1006: true, 1015: true, 1016: true, 2004: true}

// modeTracker follows the program's output for the replayed modes it turned on or off, across chunk boundaries.
type modeTracker struct {
	state  int // modeGround, modeEsc, modeCSI (after "ESC [") or modeParams (after "ESC [ ?")
	params []byte
	on     map[int]bool // every replayed mode the program has set or reset, and which way
	order  []int        // the modes in the order they were first seen
}

const (
	modeGround = iota
	modeEsc
	modeCSI
	modeParams
)

// feed reads a chunk of the program's output.
func (m *modeTracker) feed(chunk []byte) {
	for _, b := range chunk {
		switch m.state {
		case modeGround:
			if b == 0x1b {
				m.state = modeEsc
			}
		case modeEsc:
			if b == '[' {
				m.state = modeCSI
			} else {
				m.state = modeGround
				if b == 0x1b {
					m.state = modeEsc
				}
			}
		case modeCSI:
			if b == '?' {
				m.state = modeParams
				m.params = m.params[:0]
			} else {
				m.state = modeGround
				if b == 0x1b {
					m.state = modeEsc
				}
			}
		case modeParams:
			switch {
			case b >= '0' && b <= '9' || b == ';':
				if len(m.params) < 64 {
					m.params = append(m.params, b)
				}
			case b == 'h' || b == 'l':
				m.apply(b == 'h')
				m.state = modeGround
			case b == 0x1b:
				m.state = modeEsc
			default:
				m.state = modeGround
			}
		}
	}
}

func (m *modeTracker) apply(set bool) {
	for _, field := range strings.Split(string(m.params), ";") {
		mode, err := strconv.Atoi(field)
		if err != nil || !replayedModes[mode] {
			continue
		}
		if m.on == nil {
			m.on = map[int]bool{}
		}
		if _, seen := m.on[mode]; !seen {
			m.order = append(m.order, mode)
		}
		m.on[mode] = set
	}
}

// replay is the escape sequences that put a terminal that has only seen the program's recent output in the modes the
// program left it in. Empty when the program has not touched any.
func (m *modeTracker) replay() []byte {
	var out []byte
	for _, mode := range m.order {
		out = append(out, "\x1b[?"+strconv.Itoa(mode)...)
		if m.on[mode] {
			out = append(out, 'h')
		} else {
			out = append(out, 'l')
		}
	}
	return out
}

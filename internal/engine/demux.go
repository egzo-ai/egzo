// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package engine

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// MaxFrame is the largest frame Demux reads. An engine frames what a container writes as it writes it, in pieces of a few dozen
// KiB at most (a log line is split at 16 KiB); a frame header that claims more is junk, or an engine that wants the reader to
// allocate what it says.
const MaxFrame = 1 << 20

// ErrFrameTooLarge is what Demux returns for a frame header that claims more than MaxFrame.
type ErrFrameTooLarge struct{ Size uint32 }

func (e ErrFrameTooLarge) Error() string {
	return fmt.Sprintf("the engine sent a stream frame of %d bytes, more than the %d this reader takes: refusing the stream", e.Size, MaxFrame)
}

// Demux splits a multiplexed stream (a container's logs, or what a command run without a terminal says) into standard output and
// standard error, like the engine client's stdcopy.StdCopy, which allocates whatever size a frame header claims. It returns the
// bytes written, and nil at the end of the stream, even one that ends in a frame (a reader cut off at a limit): that frame is dropped.
func Demux(stdout, stderr io.Writer, src io.Reader) (written int64, err error) {
	var header [8]byte
	buf := make([]byte, 32<<10)
	for {
		if _, err := io.ReadFull(src, header[:]); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return written, nil
			}
			return written, err
		}
		size := binary.BigEndian.Uint32(header[4:])
		if size > MaxFrame {
			return written, ErrFrameTooLarge{Size: size}
		}
		if int(size) > len(buf) {
			buf = make([]byte, size)
		}
		frame := buf[:size]
		if _, err := io.ReadFull(src, frame); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return written, nil
			}
			return written, err
		}
		var out io.Writer
		switch header[0] {
		case 0, 1:
			out = stdout
		case 2:
			out = stderr
		case 3:
			return written, fmt.Errorf("error from the engine in stream: %s", frame)
		default:
			return written, fmt.Errorf("the engine sent a stream frame of unknown type %d", header[0])
		}
		n, err := out.Write(frame)
		written += int64(n)
		if err != nil {
			return written, err
		}
	}
}

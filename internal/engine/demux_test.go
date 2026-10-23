// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package engine

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"runtime"
	"strings"
	"testing"
)

func frameOf(stream byte, data string) []byte {
	header := []byte{stream, 0, 0, 0, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(header[4:], uint32(len(data)))
	return append(header, data...)
}

func TestDemuxSplitsTheTwoStreams(t *testing.T) {
	var src bytes.Buffer
	src.Write(frameOf(1, "out "))
	src.Write(frameOf(2, "err"))
	src.Write(frameOf(1, strings.Repeat("x", 100<<10)))
	var out, errs bytes.Buffer
	n, err := Demux(&out, &errs, &src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "out x") || out.Len() != 4+100<<10 || errs.String() != "err" || n != int64(out.Len()+errs.Len()) {
		t.Errorf("out %d bytes, err %q, written %d", out.Len(), errs.String(), n)
	}
}

func TestDemuxRefusesAFrameLargerThanMaxFrameWithoutAllocatingIt(t *testing.T) {
	header := []byte{1, 0, 0, 0, 0xff, 0xff, 0xff, 0xf0}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := Demux(io.Discard, io.Discard, bytes.NewReader(append(header, "short"...)))
	runtime.ReadMemStats(&after)
	var tooLarge ErrFrameTooLarge
	if !errors.As(err, &tooLarge) || tooLarge.Size != 0xfffffff0 {
		t.Fatalf("err = %v", err)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 1<<20 {
		t.Errorf("allocated %d bytes for a frame it refused", grew)
	}
}

func TestDemuxEndsQuietlyWhereTheStreamIsCutOff(t *testing.T) {
	stream := append(frameOf(1, "whole"), frameOf(1, "cut off here")...)
	var out bytes.Buffer
	if _, err := Demux(&out, io.Discard, bytes.NewReader(stream[:len(stream)-4])); err != nil || out.String() != "whole" {
		t.Errorf("out %q, err %v", out.String(), err)
	}
}

func TestDemuxReportsAnErrorTheEngineSendsInTheStream(t *testing.T) {
	if _, err := Demux(io.Discard, io.Discard, bytes.NewReader(frameOf(3, "boom"))); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v", err)
	}
}

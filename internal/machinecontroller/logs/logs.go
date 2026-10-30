// Package logs reads the logs of a worker container, line by line, redacted: the connectors log the command lines
// of the tools, with the credentials of the API under test, so no line leaves the host as it is.
package logs

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/controllerapi"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/docker"
	"github.com/anovimous/automata-worker-monitor/internal/redact"
)

const (
	// a longer line is cut, the rest of it dropped
	maxLine = 64 << 10
	// DefaultTail and MaxTail bound how many lines are read back before following
	DefaultTail = 500
	MaxTail     = 5000
)

type Options struct {
	Tail   int
	Since  string // RFC 3339, or "" for no limit
	Follow bool
}

// Stream reads the logs of a container and gives emit each line, redacted, until the logs end (or, with Follow,
// until ctx is done). An error of emit stops it and is returned.
func Stream(ctx context.Context, engine docker.Engine, containerID string, opts Options, emit func(controllerapi.LogLine) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	rc, err := engine.Logs(ctx, containerID, docker.LogOptions{
		Tail: strconv.Itoa(opts.Tail), Since: opts.Since, Follow: opts.Follow, Timestamps: true,
	})
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		rc.Close()
	}()

	outR, outW := io.Pipe()
	errR, errW := io.Pipe()
	go func() {
		err := docker.Demux(outW, errW, rc)
		outW.CloseWithError(err)
		errW.CloseWithError(err)
	}()

	lines := make(chan controllerapi.LogLine)
	done := make(chan struct{}, 2)
	for _, src := range []struct {
		r      io.Reader
		stream string
	}{{outR, "stdout"}, {errR, "stderr"}} {
		go func() {
			defer func() { done <- struct{}{} }()
			readLines(src.r, func(raw []byte) bool {
				select {
				case lines <- parse(raw, src.stream):
					return true
				case <-ctx.Done():
					return false
				}
			})
		}()
	}

	for open := 2; open > 0; {
		select {
		case line := <-lines:
			if err := emit(line); err != nil {
				return err
			}
		case <-done:
			open--
		case <-ctx.Done():
			return nil
		}
	}
	// the readers are done, but a line may still be waiting
	for {
		select {
		case line := <-lines:
			if err := emit(line); err != nil {
				return err
			}
		default:
			return nil
		}
	}
}

// readLines calls fn with each line of r, without its newline and cut at maxLine; fn returns false to stop.
func readLines(r io.Reader, fn func([]byte) bool) {
	br := bufio.NewReaderSize(r, maxLine)
	for {
		line, err := br.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			// keep the start of the line, skip the rest of it
			kept := append([]byte(nil), line...)
			for errors.Is(err, bufio.ErrBufferFull) {
				_, err = br.ReadSlice('\n')
			}
			if !fn(kept) {
				return
			}
			if err != nil {
				return
			}
			continue
		}
		if len(line) > 0 {
			if !fn(bytes.TrimRight(line, "\r\n")) {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// parse reads the timestamp Docker puts first (Timestamps) and redacts the rest.
func parse(raw []byte, stream string) controllerapi.LogLine {
	text := string(raw)
	line := controllerapi.LogLine{Stream: stream}
	if ts, rest, ok := strings.Cut(text, " "); ok {
		if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			line.TS, text = t.UTC(), rest
		}
	}
	line.Line = redact.Line(text)
	return line
}

// Tail bounds a requested tail: missing or below 1 is DefaultTail, above MaxTail is MaxTail.
func Tail(requested int) int {
	switch {
	case requested <= 0:
		return DefaultTail
	case requested > MaxTail:
		return MaxTail
	}
	return requested
}

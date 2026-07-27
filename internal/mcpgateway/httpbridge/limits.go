package httpbridge

// Enforces message framing limits before bytes reach SDK decoders.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ErrMessageTooLarge reports that one MCP JSON message or SSE event exceeded
// the bridge's configured byte limit.
var ErrMessageTooLarge = errors.New("MCP message exceeds configured byte limit")

type framingMode uint8

const (
	framingBody framingMode = iota
	framingEvent
)

type lineLimitReadCloser struct {
	source   io.ReadCloser
	reader   *bufio.Reader
	limit    int
	pending  []byte
	terminal error
}

type framingLimitReadCloser struct {
	source  io.ReadCloser
	reader  *bufio.Reader
	limit   int
	mode    framingMode
	onLimit func()

	frameBytes int
	lineBytes  int
	lastByte   byte
	terminal   error
}

var (
	_ io.ReadCloser = (*framingLimitReadCloser)(nil)
	_ io.ReadCloser = (*lineLimitReadCloser)(nil)
)

func newLineLimitReadCloser(
	source io.ReadCloser,
	limit int,
) io.ReadCloser {
	return &lineLimitReadCloser{
		source: source,
		reader: bufio.NewReader(source),
		limit:  limit,
	}
}

func newResponseLimitReadCloser(
	source io.ReadCloser,
	limit int,
	event bool,
	onLimit func(),
) io.ReadCloser {
	mode := framingBody
	if event {
		mode = framingEvent
	}
	return newFramingLimitReadCloser(source, limit, mode, onLimit)
}

func newFramingLimitReadCloser(
	source io.ReadCloser,
	limit int,
	mode framingMode,
	onLimit func(),
) *framingLimitReadCloser {
	return &framingLimitReadCloser{
		source:  source,
		reader:  bufio.NewReader(source),
		limit:   limit,
		mode:    mode,
		onLimit: onLimit,
	}
}

func (r *framingLimitReadCloser) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	if r.terminal != nil {
		return 0, r.terminal
	}

	count := 0
	for count < len(buffer) {
		if r.frameBytes == r.limit {
			_, err := r.reader.ReadByte()
			if err != nil {
				r.terminal = err
				return count, err
			}

			r.terminal = messageLimitError(r.limit)
			if r.onLimit != nil {
				r.onLimit()
			}
			return count, r.terminal
		}

		value, err := r.reader.ReadByte()
		if err != nil {
			r.terminal = err
			return count, err
		}

		buffer[count] = value
		count++
		r.frameBytes++
		r.observeFramingByte(value)

		if r.reader.Buffered() == 0 {
			return count, nil
		}
	}

	return count, nil
}

func (r *framingLimitReadCloser) Close() error {
	return r.source.Close()
}

func (r *framingLimitReadCloser) observeFramingByte(value byte) {
	if r.mode != framingEvent {
		return
	}
	if value != '\n' {
		r.lineBytes++
		r.lastByte = value
		return
	}

	blank := r.lineBytes == 0 ||
		(r.lineBytes == 1 && r.lastByte == '\r')
	r.lineBytes = 0
	r.lastByte = 0
	if blank {
		r.frameBytes = 0
	}
}

func messageLimitError(limit int) error {
	return fmt.Errorf("%w: limit is %d bytes", ErrMessageTooLarge, limit)
}

func (r *lineLimitReadCloser) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	if len(r.pending) == 0 {
		if r.terminal != nil {
			return 0, r.terminal
		}
		if err := r.readFrame(); err != nil {
			return 0, err
		}
	}

	count := copy(buffer, r.pending)
	r.pending = r.pending[count:]
	return count, nil
}

func (r *lineLimitReadCloser) Close() error {
	return r.source.Close()
}

func (r *lineLimitReadCloser) readFrame() error {
	frame := make([]byte, 0, min(r.limit, 32*1024))
	for {
		chunk, err := r.reader.ReadSlice('\n')
		frame = append(frame, chunk...)

		payloadBytes := len(frame)
		if len(frame) > 0 && frame[len(frame)-1] == '\n' {
			payloadBytes--
		}
		if payloadBytes > r.limit {
			r.terminal = messageLimitError(r.limit)
			return r.terminal
		}

		switch {
		case err == nil:
			return r.acceptFrame(frame, nil)
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(frame) > 0:
			return r.acceptFrame(frame, io.EOF)
		case err != nil:
			r.terminal = err
			return err
		}
	}
}

func (r *lineLimitReadCloser) acceptFrame(frame []byte, terminal error) error {
	if !json.Valid(frame) {
		r.terminal = errors.New(
			"downstream MCP frame must contain one complete JSON value",
		)
		return r.terminal
	}

	r.pending = frame
	r.terminal = terminal
	return nil
}

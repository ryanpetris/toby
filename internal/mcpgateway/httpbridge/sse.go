package httpbridge

// Receives server-initiated JSON-RPC messages from the standalone SSE stream.

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

const (
	minimumEventRetry = 100 * time.Millisecond
	maximumEventRetry = 30 * time.Second
)

var (
	errEventProtocol = errors.New("invalid MCP event stream")
	errEventConsumer = errors.New("MCP event consumer failed")
)

type eventCursor struct {
	lastEventID string
	retry       time.Duration
}

func scanEventMessages(
	reader io.Reader,
	maxEventSize int,
	cursor *eventCursor,
	yield func(jsonrpc.Message) error,
) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), maxEventSize)

	var eventName string
	var eventID *string
	var data bytes.Buffer
	dispatch := func() error {
		if eventID != nil {
			cursor.lastEventID = *eventID
		}
		if data.Len() == 0 {
			eventName = ""
			eventID = nil
			return nil
		}

		payload := data.Bytes()
		payload = bytes.TrimSuffix(payload, []byte{'\n'})
		if eventName != "" && eventName != "message" {
			eventName = ""
			eventID = nil
			data.Reset()
			return nil
		}

		message, err := jsonrpc.DecodeMessage(payload)
		if err != nil {
			return fmt.Errorf("%w: decode JSON-RPC message: %v", errEventProtocol, err)
		}
		if err := yield(message); err != nil {
			return errors.Join(errEventConsumer, err)
		}

		eventName = ""
		eventID = nil
		data.Reset()
		return nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}

		field, value, found := strings.Cut(line, ":")
		if !found {
			return fmt.Errorf("%w: malformed SSE field", errEventProtocol)
		}
		value = strings.TrimPrefix(value, " ")

		switch field {
		case "event":
			eventName = value
		case "id":
			if !strings.ContainsRune(value, '\x00') {
				candidate := value
				eventID = &candidate
			}
		case "retry":
			milliseconds, err := strconv.ParseUint(value, 10, 63)
			if err == nil {
				maximumMilliseconds := uint64(
					maximumEventRetry / time.Millisecond,
				)
				milliseconds = min(milliseconds, maximumMilliseconds)
				cursor.retry = boundedEventRetry(
					time.Duration(milliseconds) * time.Millisecond,
				)
			}
		case "data":
			if data.Len()+len(value)+1 > maxEventSize {
				return messageLimitError(maxEventSize)
			}
			data.WriteString(value)
			data.WriteByte('\n')
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return messageLimitError(maxEventSize)
		}
		return err
	}

	return dispatch()
}

func boundedEventRetry(retry time.Duration) time.Duration {
	return min(max(retry, minimumEventRetry), maximumEventRetry)
}

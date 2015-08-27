package oplogc

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// ErrIncompleteEvent is returned when the decoder only recieved a partial event
var ErrIncompleteEvent = errors.New("incomplete event")

// ErrInvalidEvent is returned when the decoder was not able to unmarshal the event
var ErrInvalidEvent = errors.New("invalid event")

// ErrConnectionClosed when the SSE stream has closed unexpectedly
var ErrConnectionClosed = errors.New("connection closed")

type decoder struct {
	*bufio.Reader
}

func newDecoder(r io.Reader) *decoder {
	return &decoder{bufio.NewReader(r)}
}

// next reads the next operation from a SSE stream or block until one comes in.
func (d *decoder) next(op *Operation) (err error) {
	// Reset non reusable fields
	op.Event = ""
	op.Data = nil

	var line string
	started := false

	for {
		if line, err = d.ReadString('\n'); err != nil {
			err = ErrConnectionClosed
			break
		}
		if line == "\n" {
			if started {
				// Message is complete, send it
				break
			}
			continue
		}
		line = strings.TrimSuffix(line, "\n")
		if strings.HasPrefix(line, ":") {
			// Comment, ignore
			continue
		}
		started = true
		sections := strings.SplitN(line, ":", 2)
		field, value := sections[0], ""
		if len(sections) == 2 {
			value = strings.TrimPrefix(sections[1], " ")
		}
		switch field {
		case "id":
			op.ID = value
		case "event":
			op.Event = value
		case "data":
			// The oplog does never return data on serveral lines
			if err = json.Unmarshal([]byte(value), &op.Data); err != nil {
				err = ErrInvalidEvent
				break
			}
		}
	}

	if err == nil && op.Event == "" {
		err = ErrIncompleteEvent
	}
	if !op.validate() {
		err = ErrInvalidEvent
	}

	return
}

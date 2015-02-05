// Package oplogc provides an easy to use client interface for the oplog service.
package oplogc

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Options is the subscription options
type Options struct {
	// Path of the state file where to persiste the current oplog position.
	// If empty string, the state is not stored.
	StateFile string
	// AllowReplication activates replication if the state file is not found.
	// When false, a consumer with no state file will only get future operations.
	AllowReplication bool
	// Password to access password protected oplog
	Password string
	// Filters to apply on the oplog output
	Filter Filter
}

// Filter contains arguments to filter the oplog output
type Filter struct {
	// A list of types to filter on
	Types []string
	// A list of parent type/id to filter on
	Parents []string
}

// Consumer holds all the information required to connect to an oplog server
type Consumer struct {
	// URL of the oplog
	url string
	// options for the consumer's subscription
	options Options
	// lastId is the current most advanced acked event id
	lastId string
	// saved is true when current lastId is persisted
	saved bool
	// processing is true when a process loop is in progress
	processing bool
	// mu is a mutex used to coordinate access to lastId and saved properties
	mu *sync.RWMutex
	// http is the client used to connect to the oplog
	http http.Client
	// body points to the current streamed response body
	body io.ReadCloser
	// ife holds all event ids sent to the consumer but no yet acked
	ife *InFlightEvents
	// ack is a channel to ack the operations
	ack chan Operation
	// stop is a channel used to stop the process loop
	stop chan struct{}
}

// ErrAccessDenied is returned by Subscribe when the oplog requires a password
// different from the one provided in options.
var ErrAccessDenied = errors.New("invalid credentials")

// ErrResumeFailed is returned when the requested last id was not found by the
// oplog server. This may happen when the last id is very old or size of the
// oplog capped collection is too small for the load.
//
// When this error happen, the consumer may choose to either ignore the lost events
// or force a full replication.
var ErrResumeFailed = errors.New("resume failed")

// ErrorWritingState is returned when the last processed id can't be written to
// the state file.
var ErrWritingState = errors.New("writing state file failed")

// Subscribe creates a Consumer to connect to the given URL.
func Subscribe(url string, options Options) *Consumer {
	qs := ""
	if len(options.Filter.Parents) > 0 {
		parents := strings.Join(options.Filter.Parents, ",")
		if parents != "" {
			qs += "?parents="
			qs += parents
		}
	}
	if len(options.Filter.Types) > 0 {
		types := strings.Join(options.Filter.Types, ",")
		if types != "" {
			if qs == "" {
				qs += "?"
			} else {
				qs += "&"
			}
			qs += "types="
			qs += types
		}
	}

	c := &Consumer{
		url:     strings.Join([]string{url, qs}, ""),
		options: options,
		ife:     NewInFlightEvents(),
		mu:      &sync.RWMutex{},
		ack:     make(chan Operation),
	}

	return c
}

// Start reads the oplog output and send operations back thru the returned ops channel.
// The caller must then call the Done() method on operation when it has been handled.
// Failing to call Done() the operations would prevent any resume in case of connection
// failure or restart of the process.
//
// Any errors are return on the errs channel. In all cases, the Start() method will
// try to reconnect and/or ignore the error. It is the callers responsability to stop
// the process loop by calling the Stop() method.
//
// When the loop has ended, a message is sent thru the done channel.
func (c *Consumer) Start() (ops chan Operation, errs chan error, done chan bool) {
	ops = make(chan Operation)
	errs = make(chan error)
	done = make(chan bool)

	// Ensure we never have more than one process loop running
	if c.processing {
		panic("Can't run two process loops in parallel")
	}
	c.processing = true

	c.mu.Lock()
	c.stop = make(chan struct{})
	stop := c.stop
	c.mu.Unlock()

	// Recover the last event id saved from a previous excution
	lastId, err := c.loadLastEventID()
	if err != nil {
		errs <- err
		return
	}
	c.lastId = lastId

	wg := sync.WaitGroup{}

	// SSE stream reading
	stopReadStream := make(chan struct{}, 1)
	wg.Add(1)
	go c.readStream(ops, errs, stopReadStream, &wg)

	// Periodic (non blocking) saving of the last id when needed
	stopStateSaving := make(chan struct{}, 1)
	if c.options.StateFile != "" {
		wg.Add(1)
		go c.periodicStateSaving(errs, stopStateSaving, &wg)
	}

	go func() {
		for {
			select {
			case <-stop:
				// If a stop is requested, we ensure all go routines are stopped
				close(stopReadStream)
				close(stopStateSaving)
				if c.body != nil {
					// Closing the body will ensure readStream isn't blocked in IO wait
					c.body.Close()
				}
				wg.Wait()
				c.processing = false
				done <- true
				return
			case op := <-c.ack:
				if op.Event == "reset" {
					c.ife.Unlock()
				}
				if idx := c.ife.Pull(op.ID); idx == 0 {
					c.SetLastId(op.ID)
				}
			}
		}
	}()

	return
}

// Stop instructs the Start() loop to stop
func (c *Consumer) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.stop != nil {
		close(c.stop)
		c.stop = nil
	}
}

// readStream maintains a connection to the oplog stream and read sent events as they are coming
func (c *Consumer) readStream(ops chan<- Operation, errs chan<- error, stop <-chan struct{}, wg *sync.WaitGroup) {
	defer wg.Done()

	c.connect()
	d := NewDecoder(c.body)
	op := Operation{}
	op.ack = c.ack
	backoff := time.Second
	for {
		err := d.Next(&op)
		select {
		case <-stop:
			return
		default:
			// proceed
		}
		if err != nil {
			errs <- err
			for {
				time.Sleep(backoff)
				if backoff < 30*time.Second {
					backoff *= 2
				}
				if err = c.connect(); err == nil {
					d = NewDecoder(c.body)
					break
				}
				errs <- err
			}
			continue
		}

		c.ife.Push(op.ID)
		if op.Event == "reset" {
			// We must not process any further operation until the "reset" operation
			// is not acke
			c.ife.Lock()
		}
		select {
		case <-stop:
			return
		default:
			ops <- op
		}

		// reset backoff on success
		backoff = time.Second
	}
}

// periodicStateSaving saves the lastId into a file every seconds if it has been updated
func (c *Consumer) periodicStateSaving(errs chan<- error, stop <-chan struct{}, wg *sync.WaitGroup) {
	defer wg.Done()

	for {
		select {
		case <-stop:
			return
		case <-time.After(time.Second):
			c.mu.RLock()
			saved := c.saved
			lastId := c.lastId
			c.mu.RUnlock()
			if saved {
				continue
			}
			if err := c.saveLastEventID(lastId); err != nil {
				errs <- ErrWritingState
			}
			c.mu.Lock()
			c.saved = lastId == c.lastId
			c.mu.Unlock()
		}
	}
}

// LastId returns the most advanced acked event id
func (c *Consumer) LastId() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastId
}

// SetLastId sets the last id to the given value and informs the save go routine
func (c *Consumer) SetLastId(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastId = id
	c.saved = false
}

// connect tries to connect to the oplog event stream
func (c *Consumer) connect() (err error) {
	if c.body != nil {
		c.body.Close()
	}
	// Usable dummy body in case of connection error
	c.body = ioutil.NopCloser(bytes.NewBuffer([]byte{}))

	req, err := http.NewRequest("GET", c.url, nil)
	if err != nil {
		return
	}
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Accept", "text/event-stream")
	lastId := c.LastId()
	if len(lastId) > 0 {
		req.Header.Set("Last-Event-ID", lastId)
	}
	if c.options.Password != "" {
		req.SetBasicAuth("", c.options.Password)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return
	}
	if lastId != "" && res.Header.Get("Last-Event-ID") != lastId {
		// If the response doesn't contain the requested Last-Event-ID
		// header, it means the resume did fail. This is not a recoverable
		// error, the operator must either decide to perform a full replication
		// or accept to loose events by truncating the state file.
		err = ErrResumeFailed
		return
	}
	if res.StatusCode == 403 || res.StatusCode == 401 {
		err = ErrAccessDenied
		return
	}
	if res.StatusCode != 200 {
		message, _ := ioutil.ReadAll(res.Body)
		err = fmt.Errorf("HTTP error %d: %s", res.StatusCode, string(message))
		return
	}
	c.body = res.Body
	return
}

// loadLastEventID tries to read the last event id from the state file.
//
// If the StateFile option was not set, the id will always be an empty string
// as for tailing only future events.
//
// If the StateFile option is set but no file exists, the last event id is
// initialized to "0" in order to request a full replication if AllowReplication
// option is set to true or to an empty string otherwise (start at present).
func (c *Consumer) loadLastEventID() (id string, err error) {
	if c.options.StateFile == "" {
		return "", nil
	}
	_, err = os.Stat(c.options.StateFile)
	if os.IsNotExist(err) {
		if c.options.AllowReplication {
			// full replication
			id = "0"
		} else {
			// start at NOW()
			id = ""
		}
		err = nil
	} else if err == nil {
		var content []byte
		content, err = ioutil.ReadFile(c.options.StateFile)
		if err != nil {
			return
		}
		if match, _ := regexp.Match("^(?:[0-9]{0,13}|[0-9a-f]{24})$", content); !match {
			err = errors.New("state file contains invalid data")
		}
		id = string(content)
	}
	return
}

// saveLastEventID persiste the last event id into a file
func (c *Consumer) saveLastEventID(id string) error {
	return ioutil.WriteFile(c.options.StateFile, []byte(id), 0644)
}

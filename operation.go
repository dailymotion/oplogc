package oplogc

import "time"

type Operation struct {
	// ID holds the operation id used to resume the streaming in case of connection failure.
	ID string
	// Event is the kind of operation. It can be insert, update or delete.
	Event string
	// Data holds the operation metadata.
	Data *OperationData
	ack  chan<- Operation
}

// OperationData is the data part of the SSE event for the operation.
type OperationData struct {
	// ID is the object id.
	ID string `json:"id"`
	// Type is the object type.
	Type string `json:"type"`
	// Ref contains the URL to fetch to object refered by the operation. This field may
	// not be present if the oplog server is not configured to generate this field.
	Ref string `json:"ref,omitempty"`
	// Timestamp is the time when the operation happened.
	Timestamp time.Time `json:"timestamp"`
	// Parents is a list of strings describing the objects related to the object
	// refered by the operation.
	Parents []string `json:"parents"`
}

// Done must be called once the operation has been processed by the consumer
func (o *Operation) Done() {
	o.ack <- *o
}

func (o *Operation) Validate() bool {
	if o.Event == "" {
		return false
	}
	if o.Data == nil && o.Event != "reset" && o.Event != "live" {
		return false
	}
	return true
}

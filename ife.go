package oplogc

import "sync"

type inFlightEvents struct {
	sync.RWMutex
	// ids is the list of in flight event IDs
	ids []string
}

// newInFlightEvents contains events ids which have been received but not yet acked
func newInFlightEvents() *inFlightEvents {
	return &inFlightEvents{
		ids: []string{},
	}
}

// count returns the number of events in flight.
func (ife *inFlightEvents) count() int {
	ife.RLock()
	defer ife.RUnlock()
	return len(ife.ids)
}

// push adds a new event id to the IFE
func (ife *inFlightEvents) push(id string) {
	ife.Lock()
	defer ife.Unlock()

	for _, eid := range ife.ids {
		if eid == id {
			// do not push the id if already in
			return
		}
	}

	ife.ids = append(ife.ids, id)
}

// pull pulls the given id from the list and returns the index
// of the pulled element in the queue. If the element wasn't found
// the index is set to -1.
func (ife *inFlightEvents) pull(id string) (index int) {
	ife.Lock()
	defer ife.Unlock()
	index = -1

	for i, eid := range ife.ids {
		if eid == id {
			index = i
			ife.ids = append(ife.ids[:i], ife.ids[i+1:]...)
			break
		}
	}

	return
}

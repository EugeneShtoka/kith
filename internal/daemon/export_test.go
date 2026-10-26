package daemon

// Test-only accessors.

// MessageSubscribers counts every message-stream reader, the notifier included.
func (s *Streams) MessageSubscribers() int { return s.messages.subscribers() }

// NarrowingCalls is every call a profile narrows, by method name.
func NarrowingCalls() []string {
	var out []string
	for name, reach := range reaches {
		if reach == narrows {
			out = append(out, name)
		}
	}
	return out
}

// Published is a notice with the topics it was published to.
type Published struct {
	Notice
	Topics []string
}

// Publications subscribes to what n publishes; take returns what arrived since it was
// last called, without waiting: a notice is queued before Deliver returns.
func (n *Notifications) Publications() (take func() []Published, release func()) {
	id, ch := n.notices.subscribeWith(notifierBuffer)
	take = func() []Published {
		var out []Published
		for {
			select {
			case v := <-ch:
				out = append(out, Published{Notice: Notice{ID: v.id, Alert: v.alert, Code: v.code}, Topics: v.topics})
			default:
				return out
			}
		}
	}
	return take, func() { n.notices.release(id) }
}

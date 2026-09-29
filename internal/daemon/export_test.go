package daemon

// What the tests wait on and count; nothing in the daemon reads these.

// MessageSubscribers counts every message-stream reader, including the notifier;
// tests use it to wait for a subscription to land.
func (s *Streams) MessageSubscribers() int { return s.messages.subscribers() }

// FollowSubscribers reports how many clients could be handed a link.
func (s *Streams) FollowSubscribers() int { return s.follows.subscribers() }

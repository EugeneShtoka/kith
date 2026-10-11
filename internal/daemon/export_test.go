package daemon

// Test-only accessors.

// MessageSubscribers counts every message-stream reader, the notifier included.
func (s *Streams) MessageSubscribers() int { return s.messages.subscribers() }

// FollowSubscribers reports how many clients could be handed a link.
func (s *Streams) FollowSubscribers() int { return s.follows.subscribers() }

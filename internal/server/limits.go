package server

type Limits struct{ Sessions, Blocks, Windows, Panes int }

func (s *Server) SetLimits(limits Limits) error {
	if limits.Sessions < 0 || limits.Blocks < 0 || limits.Windows < 0 || limits.Panes < 0 {
		return bad("resource limits must be non-negative")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.limits = limits
	return nil
}

func exceeds(count, maximum int) bool { return maximum > 0 && count > maximum }

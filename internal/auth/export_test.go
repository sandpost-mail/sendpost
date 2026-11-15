package auth

import "time"

// SetClock replaces the clock of s, for tests that drive session expiry.
func SetClock(s *Service, now func() time.Time) { s.now = now }

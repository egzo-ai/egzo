package engines

import "time"

// SetClock lets tests move time.
func SetClock(r *Registry, now func() time.Time) { r.now = now }

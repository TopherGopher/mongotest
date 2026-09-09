package dockerapi

import "time"

// now and since are indirections so tests can pin durations if needed.
var (
	now   = time.Now
	since = time.Since
)

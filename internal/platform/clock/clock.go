// Package clock is the only place allowed to read the system time. Code that
// needs the current time takes a Clock so cutoffs and deadlines can be tested
// with a fake clock.
package clock

import (
	"fmt"
	"sync"
	"time"
	_ "time/tzdata" // Embed the tz database: Windows and slim images lack Asia/Jakarta.
)

// Jakarta is the business time zone (WIB). Timestamps are stored in UTC and
// converted to Jakarta for display, production dates, and job schedules.
var Jakarta *time.Location

func init() {
	loc, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		// Unreachable while time/tzdata is embedded.
		panic(fmt.Sprintf("clock: load Asia/Jakarta: %v", err))
	}
	Jakarta = loc
}

// Clock reports the current time.
type Clock interface {
	Now() time.Time
}

// Real reads the system clock.
type Real struct{}

// Now returns the current system time.
func (Real) Now() time.Time { return time.Now() }

// Fake is a manually driven clock for tests. It is safe for concurrent use.
type Fake struct {
	mu  sync.Mutex
	now time.Time
}

// NewFake returns a fake clock frozen at now.
func NewFake(now time.Time) *Fake {
	return &Fake{now: now}
}

// Now returns the fake clock's current time.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Set moves the fake clock to t.
func (f *Fake) Set(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = t
}

// Advance moves the fake clock forward by d.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

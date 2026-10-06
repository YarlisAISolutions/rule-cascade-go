package rulecascade

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// HolderOptions configures a Holder. Without Interval or Cron the rules change only on Refresh.
type HolderOptions struct {
	// Interval refreshes at a fixed delay after the end of the previous refresh.
	Interval time.Duration
	// Cron refreshes at every fire time of a five-field cron expression (see ParseCron). It is used
	// when Interval is zero.
	Cron string
	// Location is the time zone of Cron. Default UTC.
	Location *time.Location
	// OnReload is called after every refresh, scheduled or not, on the goroutine that ran it.
	OnReload func(Reload)
}

// Reload is what a refresh did: the rules now held, whether they replaced rules with another checksum,
// and the error that kept the previous rules in place.
type Reload struct {
	RuleSet *RuleSet
	Changed bool
	Err     error
}

// Holder holds the RuleSet a service enforces and refreshes it on a schedule.
//
//	rules, err := rulecascade.NewHolder(func() (*rulecascade.RuleSet, error) {
//		data, err := os.ReadFile("transfer.bundle.json")
//		if err != nil {
//			return nil, err
//		}
//		bundle, err := rulecascade.ParseJSON(data)
//		if err != nil {
//			return nil, err
//		}
//		return rulecascade.FromBundle(bundle)
//	}, rulecascade.HolderOptions{Interval: 5 * time.Minute})
//	if err != nil {
//		log.Fatal(err) // the first load failed: do not start
//	}
//	defer rules.Close()
//	result, err := rules.Get().Evaluate(request, "server", nil)
//
// Every refresh calls the load function; when it fails, the last good rules stay in place. A ruleset
// with the checksum already held is not swapped in. Get never blocks and is safe for concurrent use.
type Holder struct {
	load     func() (*RuleSet, error)
	options  HolderOptions
	current  atomic.Pointer[RuleSet]
	mu       sync.Mutex // one refresh at a time
	done     chan struct{}
	stopped  sync.WaitGroup
	closeOne sync.Once
	nextRun  atomic.Pointer[time.Time]
}

// NewHolder loads the rules once, now, and starts the schedule. It returns the load function's error
// when that first load fails, and an error for a Cron that does not parse.
func NewHolder(load func() (*RuleSet, error), options HolderOptions) (*Holder, error) {
	if load == nil {
		return nil, errors.New("a load function is required")
	}
	if options.Interval < 0 {
		return nil, errors.New("the interval must not be negative")
	}
	var cron *Cron
	if options.Interval == 0 && options.Cron != "" {
		var err error
		if cron, err = ParseCron(options.Cron); err != nil {
			return nil, err
		}
	}
	if options.Location == nil {
		options.Location = time.UTC
	}
	first, err := load()
	if err != nil {
		return nil, err
	}
	if first == nil {
		return nil, errors.New("the load function returned no rules")
	}
	h := &Holder{load: load, options: options, done: make(chan struct{})}
	h.current.Store(first)
	switch {
	case options.Interval > 0:
		h.stopped.Add(1)
		go h.every(options.Interval)
	case cron != nil:
		h.stopped.Add(1)
		go h.onCron(cron)
	}
	return h, nil
}

// Get returns the rules to enforce now.
func (h *Holder) Get() *RuleSet { return h.current.Load() }

// NextRun returns when the next scheduled refresh runs, and false without a schedule or after Close.
func (h *Holder) NextRun() (time.Time, bool) {
	select {
	case <-h.done:
		return time.Time{}, false
	default:
	}
	if t := h.nextRun.Load(); t != nil {
		return *t, true
	}
	return time.Time{}, false
}

// Refresh loads the rules now. On success they replace the rules held when their checksum differs; on
// failure the rules held stay in place. Either way OnReload is called and the outcome returned.
func (h *Holder) Refresh() Reload {
	h.mu.Lock()
	previous := h.current.Load()
	loaded, err := h.load()
	if err == nil && loaded == nil {
		err = errors.New("the load function returned no rules")
	}
	reload := Reload{RuleSet: previous, Err: err}
	if err == nil && loaded.Checksum() != previous.Checksum() {
		h.current.Store(loaded)
		reload = Reload{RuleSet: loaded, Changed: true}
	}
	h.mu.Unlock()
	if h.options.OnReload != nil {
		func() {
			defer func() { _ = recover() }() // a listener cannot stop the schedule
			h.options.OnReload(reload)
		}()
	}
	return reload
}

func (h *Holder) setNext(t time.Time) { h.nextRun.Store(&t) }

func (h *Holder) every(interval time.Duration) {
	defer h.stopped.Done()
	timer := time.NewTimer(interval)
	defer timer.Stop()
	h.setNext(time.Now().Add(interval))
	for {
		select {
		case <-h.done:
			return
		case <-timer.C:
			h.Refresh()
			timer.Reset(interval)
			h.setNext(time.Now().Add(interval))
		}
	}
}

func (h *Holder) onCron(cron *Cron) {
	defer h.stopped.Done()
	for {
		next := cron.Next(time.Now().In(h.options.Location))
		if next.IsZero() {
			return
		}
		h.setNext(next)
		timer := time.NewTimer(time.Until(next))
		select {
		case <-h.done:
			timer.Stop()
			return
		case <-timer.C:
			if time.Now().Before(next) {
				continue // a timer that fired early: wait for the rest
			}
			h.Refresh()
		}
	}
}

// Close stops the schedule and waits for a refresh in progress to finish. The rules held stay available
// through Get. Calling Close more than once is safe.
func (h *Holder) Close() {
	h.closeOne.Do(func() { close(h.done) })
	h.stopped.Wait()
}

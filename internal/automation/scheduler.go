package automation

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"agentchunzhi/internal/store"
)

// Built-in cron scheduler: closes the gap where trigger.type='cron' jobs were
// stored but never executed (analysis report #16/P1 area, report_E section 3).
// The scheduler lives next to the River workers as its own goroutine ticking
// every 30 seconds; due runs are enqueued through Service.CreateScheduledRun,
// which keeps lease/retry/concurrency semantics identical to manual runs.
//
// Dedup is double-insured:
//  1. in-process windowGuard suppresses duplicate work inside one tick or a
//     burst of ticks landing in the same minute;
//  2. each window mints one structured idempotency key
//     "scheduled:<jobID>:<unix-minute>", checked against automation.runs and
//     protected by the partial unique index, so restarts or concurrent worker
//     processes cannot create two scheduled runs for the same minute window.
//
// event triggers stay untouched this batch (documented contradiction item);
// only cron and manual sources exist here.

const (
	// DefaultSchedulerInterval matches the existing River periodic cadence.
	DefaultSchedulerInterval = 30 * time.Second

	maxCronJobsPerTick = 200

	// windowGuardTTL keeps reserved keys long enough to span slow ticks while
	// still reclaiming memory; keys embed their absolute minute, so stale
	// entries would be harmless anyway.
	windowGuardTTL = 15 * time.Minute
)

// ---------------------------------------------------------------------------
// Minimal 5-field cron expression support (* , numbers, */n, lists, ranges),
// minute granularity, no timezone libraries: LoadLocation with UTC fallback.
// ---------------------------------------------------------------------------

const (
	cronMinuteCount     = 60
	cronHourCount       = 24
	cronMonthCount      = 13  // index by month, 0 unused
	cronDayOfMonthCount = 32  // index by day, 0 unused
	cronDayOfWeekCount  = 7   // 0=Sunday; 7 parsed as alias for Sunday
	cronMaxSearchYears  = 366 // NextFireAfter horizon in days
)

type CronSpec struct {
	minutes     [cronMinuteCount]bool
	hours       [cronHourCount]bool
	daysOfMonth [cronDayOfMonthCount]bool
	months      [cronMonthCount]bool
	daysOfWeek  [cronDayOfWeekCount]bool
	// Restricted marks whether the day field is an explicit constraint (not
	// "*"); both-restricted activates the standard dom/dow OR matching rule.
	domRestricted bool
	dowRestricted bool
}

// ParseCronExpression parses "<minute> <hour> <day-of-month> <month>
// <day-of-week>". Supported syntax per field: "*", numbers, ranges "a-b",
// steps "*/n"/"a-b/n", and comma lists combining any of these.
func ParseCronExpression(expression string) (*CronSpec, error) {
	fields := strings.Fields(strings.TrimSpace(expression))
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron expression must have exactly 5 whitespace-separated fields, got %d", len(fields))
	}
	minutes, _, err := parseCronField(fields[0], 0, 59)
	if err != nil {
		return nil, fmt.Errorf("minute field: %w", err)
	}
	hours, _, err := parseCronField(fields[1], 0, 23)
	if err != nil {
		return nil, fmt.Errorf("hour field: %w", err)
	}
	daysOfMonth, domStar, err := parseCronField(fields[2], 1, 31)
	if err != nil {
		return nil, fmt.Errorf("day-of-month field: %w", err)
	}
	months, _, err := parseCronField(fields[3], 1, 12)
	if err != nil {
		return nil, fmt.Errorf("month field: %w", err)
	}
	dowsRaw, dowStar, err := parseCronField(fields[4], 0, 7)
	if err != nil {
		return nil, fmt.Errorf("day-of-week field: %w", err)
	}
	spec := &CronSpec{
		domRestricted: !domStar,
		dowRestricted: !dowStar,
	}
	copy(spec.minutes[:], minutes[:cronMinuteCount])
	copy(spec.hours[:], hours[:cronHourCount])
	copy(spec.daysOfMonth[:], daysOfMonth[:cronDayOfMonthCount])
	copy(spec.months[:], months[:cronMonthCount])
	for day, set := range dowsRaw {
		// Fold Sunday alias 7 back onto 0.
		if set && day <= 7 {
			spec.daysOfWeek[day%cronDayOfWeekCount] = true
		}
	}
	return spec, nil
}

// parseCronField expands one cron field into a bitmap over [min,max].
// The second result reports whether the whole field is just "*".
func parseCronField(field string, min, max int) ([]bool, bool, error) {
	field = strings.TrimSpace(field)
	star := field == "*"
	values := make([]bool, max+1)
	for _, item := range strings.Split(field, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			return nil, false, errors.New("empty list item")
		}
		base, stepText, hasStep := strings.Cut(item, "/")
		step := 1
		if hasStep {
			parsed, convErr := strconv.Atoi(strings.TrimSpace(stepText))
			if convErr != nil || parsed < 1 {
				return nil, false, fmt.Errorf("invalid step %q", stepText)
			}
			step = parsed
		}
		base = strings.TrimSpace(base)
		lo, hi := min, max
		switch {
		case base == "*" || base == "":
			// step without explicit range sweeps the full domain
		case strings.Contains(base, "-"):
			lowerText, upperText, _ := strings.Cut(base, "-")
			var lower, upper int
			var convErr error
			if lower, convErr = strconv.Atoi(strings.TrimSpace(lowerText)); convErr != nil {
				return nil, false, fmt.Errorf("invalid range start %q", lowerText)
			}
			if upper, convErr = strconv.Atoi(strings.TrimSpace(upperText)); convErr != nil {
				return nil, false, fmt.Errorf("invalid range end %q", upperText)
			}
			if lower < min || upper > max {
				return nil, false, fmt.Errorf("range %d-%d outside [%d,%d]", lower, upper, min, max)
			}
			if lower > upper {
				return nil, false, fmt.Errorf("range %d-%d is out of order", lower, upper)
			}
			lo, hi = lower, upper
		default:
			value, convErr := strconv.Atoi(base)
			if convErr != nil {
				return nil, false, fmt.Errorf("invalid value %q", base)
			}
			if value < min || value > max {
				return nil, false, fmt.Errorf("value %d outside [%d,%d]", value, min, max)
			}
			lo, hi = value, value
		}
		for value := lo; value <= hi; value += step {
			values[value] = true
		}
	}
	return values, star, nil
}

// Matches reports whether t's wall clock (evaluated in t's location) hits the
// expression. Month/minute/hour use plain AND; day handling follows standard
// cron: both day fields restricted => OR, otherwise each side must match (an
// unconstrained "*" matches everything).
func (c *CronSpec) Matches(t time.Time) bool {
	if !c.minutes[t.Minute()] || !c.hours[t.Hour()] || !c.months[int(t.Month())] {
		return false
	}
	domMatch := c.daysOfMonth[t.Day()]
	dowMatch := c.daysOfWeek[int(t.Weekday())]
	if c.domRestricted && c.dowRestricted {
		return domMatch || dowMatch
	}
	return domMatch && dowMatch
}

// NextFireAfter returns the first matching minute strictly after the given
// instant (same location), or the zero Time if none exists within ~1 year —
// pure function kept exported for testing and future tooling.
func NextFireAfter(spec *CronSpec, after time.Time) time.Time {
	cursor := after.Truncate(time.Minute).Add(time.Minute)
	limit := cursor.AddDate(0, 0, cronMaxSearchYears)
	for cursor.Before(limit) {
		if spec.Matches(cursor) {
			return cursor
		}
		cursor = cursor.Add(time.Minute)
	}
	return time.Time{}
}

// ResolveTimezone honors the job timezone column; unknown names fall back to
// UTC instead of failing the tick.
func ResolveTimezone(name string) *time.Location {
	name = strings.TrimSpace(name)
	if name == "" {
		return time.UTC
	}
	location, err := time.LoadLocation(name)
	if err != nil || location == nil {
		return time.UTC
	}
	return location
}

// ScheduleWindowKey builds the per-minute idempotency marker handed to
// CreateScheduledRun. The Unix timestamp keeps keys absolute-time stable no
// matter which zone evaluated the expression, and guarantees the >=16 char
// length CreateRun validation demands.
func ScheduleWindowKey(jobID string, window time.Time) string {
	return ScheduledRunKeyPrefix + jobID + ":" + strconv.FormatInt(window.Unix(), 10)
}

// ---------------------------------------------------------------------------
// Persistence surface (narrow interface so Tick logic unit-tests with fakes).
// ---------------------------------------------------------------------------

type ScheduleEnqueuer interface {
	EnabledCronJobs(ctx context.Context, limit int) ([]CronJobRef, error)
	ScheduledRunWindowHandled(ctx context.Context, jobID, windowKey string) (bool, error)
	CreateScheduledRun(ctx context.Context, jobID, windowKey string) error
	RecordScheduleFailure(ctx context.Context, jobID, windowKey, errorCode, summary string) error
}

type serviceScheduleEnqueuer struct{ service Service }

// NewServiceScheduleEnqueuer adapts a Store-backed automation.Service to the
// scheduler. No WorkspacePolicy is wired because scheduled runs bypass member
// authorization on purpose.
func NewServiceScheduleEnqueuer(store *store.Store) ScheduleEnqueuer {
	return serviceScheduleEnqueuer{service: Service{Store: store}}
}

func (a serviceScheduleEnqueuer) EnabledCronJobs(ctx context.Context, limit int) ([]CronJobRef, error) {
	return a.service.EnabledCronJobs(ctx, limit)
}

func (a serviceScheduleEnqueuer) ScheduledRunWindowHandled(ctx context.Context, jobID, windowKey string) (bool, error) {
	return a.service.ScheduledRunWindowHandled(ctx, jobID, windowKey)
}

func (a serviceScheduleEnqueuer) CreateScheduledRun(ctx context.Context, jobID, windowKey string) error {
	_, err := a.service.CreateScheduledRun(ctx, jobID, windowKey)
	return err
}

func (a serviceScheduleEnqueuer) RecordScheduleFailure(ctx context.Context, jobID, windowKey, errorCode, summary string) error {
	return a.service.RecordScheduleFailure(ctx, jobID, windowKey, errorCode, summary)
}

// ---------------------------------------------------------------------------
// Scheduler loop.
// ---------------------------------------------------------------------------

// Scheduler evaluates enabled cron jobs once per tick and enqueues due runs.
type Scheduler struct {
	persist ScheduleEnqueuer
	Clock   func() time.Time // injectable clock; defaults to time.Now
	Logf    func(string, ...any)

	guard *windowGuard
}

func NewScheduler(persist ScheduleEnqueuer, logf func(string, ...any)) *Scheduler {
	return &Scheduler{persist: persist, Clock: time.Now, Logf: logf, guard: newWindowGuard()}
}

func (s *Scheduler) clock() time.Time {
	if s.Clock == nil {
		return time.Now()
	}
	return s.Clock()
}

func (s *Scheduler) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf("automation scheduler "+format, args...)
	}
}

// Run drives ticks until ctx is done. The first evaluation happens after one
// interval on purpose: worker boot should not stampede catch-up fires.
func (s *Scheduler) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = DefaultSchedulerInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.Tick(ctx); err != nil {
				s.logf("tick failed: %v", err)
			}
		}
	}
}

// Tick evaluates every enabled cron job once. Job-level problems are logged
// and skipped so a single broken expression or unreachable dependency never
// blocks other jobs; only catalog-level failures bubble up.
func (s *Scheduler) Tick(ctx context.Context) error {
	if s.persist == nil {
		return errors.New("scheduler persistence adapter is not configured")
	}
	if s.guard == nil {
		s.guard = newWindowGuard()
	}
	now := s.clock()
	jobs, err := s.persist.EnabledCronJobs(ctx, maxCronJobsPerTick)
	if err != nil {
		return fmt.Errorf("list enabled cron jobs: %w", err)
	}
	for _, ref := range jobs {
		s.tickJob(ctx, ref, now)
	}
	return nil
}

func (s *Scheduler) tickJob(ctx context.Context, ref CronJobRef, now time.Time) {
	expression := ""
	if raw, ok := ref.Trigger["expression"].(string); ok {
		expression = strings.TrimSpace(raw)
	}
	spec, parseErr := ParseCronExpression(expression)
	if parseErr != nil {
		s.logf("job=%s skipped: invalid cron expression %q: %v", ref.ID, expression, parseErr)
		return
	}
	windowMinute := now.In(ResolveTimezone(ref.Timezone)).Truncate(time.Minute)
	if !spec.Matches(windowMinute) {
		return
	}
	windowKey := ScheduleWindowKey(ref.ID, windowMinute)
	if !s.guard.Reserve(windowKey, now) {
		return // same process already handled this window
	}
	handled, err := s.persist.ScheduledRunWindowHandled(ctx, ref.ID, windowKey)
	if err != nil {
		s.logf("job=%s window lookup failed (will retry next tick): %v", ref.ID, err)
		return
	}
	if handled {
		return
	}
	if err := s.persist.CreateScheduledRun(ctx, ref.ID, windowKey); err != nil {
		// Failure becomes an observable failed run carrying error_summary; the
		// marker also consumes the window so we stop hammering a broken job.
		if recErr := s.persist.RecordScheduleFailure(ctx, ref.ID, windowKey, "schedule_failed", truncateScheduleSummary(err)); recErr != nil {
			s.logf("job=%s run creation failed (%v) and failure record also failed (%v)", ref.ID, err, recErr)
			return
		}
		s.logf("job=%s schedule failure recorded for window %s: %v", ref.ID, windowKey, err)
		return
	}
	s.logf("job=%s fired for window %s", ref.ID, windowKey)
}

func truncateScheduleSummary(err error) string {
	if err == nil {
		return ""
	}
	value := err.Error()
	if len(value) > 500 {
		return value[:500]
	}
	return value
}

// windowGuard is the in-process half of the dedup insurance: it remembers
// recently reserved keys so ticks landing twice inside the same wall-minute
// (or two overlapping ticks) enqueue nothing extra.
type windowGuard struct {
	seen map[string]time.Time
}

func newWindowGuard() *windowGuard {
	return &windowGuard{seen: make(map[string]time.Time)}
}

// Reserve records key and reports whether this caller is first for it. Entries
// older than windowGuardTTL are evicted opportunistically using the injected
// clock, keeping behavior deterministic under tests.
func (g *windowGuard) Reserve(key string, now time.Time) bool {
	for existing, reservedAt := range g.seen {
		if now.Sub(reservedAt) > windowGuardTTL {
			delete(g.seen, existing)
		}
	}
	if _, duplicate := g.seen[key]; duplicate {
		return false
	}
	g.seen[key] = now
	return true
}

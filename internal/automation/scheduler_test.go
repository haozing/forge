package automation

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
	_ "time/tzdata" // guarantee zone database availability when testing LoadLocation paths
)

// ---------------------------------------------------------------------------
// Cron parsing (table-driven)
// ---------------------------------------------------------------------------

func TestCronExpressionMatchesTableDriven(t *testing.T) {
	utc := func(minute, hour, day int, month time.Month, weekday time.Weekday, year int) time.Time {
		return time.Date(year, month, day, hour, minute, 0, 0, time.UTC)
	}
	cases := []struct {
		name       string
		expression string
		instant    time.Time
		want       bool
	}{
		{"wildcard matches anything", "* * * * *", utc(7, 3, 15, time.August, time.Saturday, 2026), true},
		{"step minutes fire on multiples of 15", "*/15 * * * *", utc(30, 9, 15, time.August, time.Saturday, 2026), true},
		{"step minutes skip non-multiples", "*/15 * * * *", utc(7, 9, 15, time.August, time.Saturday, 2026), false},
		{"list matches member", "0,30 * * * *", utc(30, 22, 15, time.August, time.Saturday, 2026), true},
		{"list skips non-member", "0,30 * * * *", utc(31, 22, 15, time.August, time.Saturday, 2026), false},
		{"exact minute and hour", "5 1 * * *", utc(5, 1, 20, time.August, time.Thursday, 2026), true},
		{"exact hour mismatch", "5 1 * * *", utc(5, 2, 20, time.August, time.Thursday, 2026), false},
		{"range dow weekdays only", "0 12 * * 1-5", utc(0, 12, 24, time.August, time.Monday, 2026), true},
		{"range dow excludes saturday", "0 12 * * 1-5", utc(0, 12, 15, time.August, time.Saturday, 2026), false},
		{"sunday alias 7 accepted", "0 12 * * 7", utc(0, 12, 23, time.August, time.Sunday, 2026), true},
		{"dom restricted fires on day", "0 0 1 * *", utc(0, 0, 1, time.January, time.Thursday, 2026), true},
		{"dom restricted skips other day", "0 0 1 * *", utc(0, 0, 2, time.January, time.Friday, 2026), false},
		{"month gate blocks", "0 0 1 1 *", utc(0, 0, 1, time.December, time.Thursday, 2026), false},
		{"month gate passes january", "0 0 1 1 *", utc(0, 0, 1, time.January, time.Thursday, 2026), true},
		{"dom+dow both restricted OR: dom hit", "0 0 15 * 1", utc(0, 0, 15, time.August, time.Saturday, 2026), true},
		{"dom+dow both restricted OR: dow hit", "0 0 15 * 1", utc(0, 0, 3, time.August, time.Monday, 2026), true},
		{"dom+dow both restricted OR: neither", "0 0 15 * 1", utc(0, 0, 18, time.August, time.Tuesday, 2026), false},
		{"step over range matches boundary", "10-30/5 * * * *", utc(20, 6, 11, time.August, time.Tuesday, 2026), true},
		{"step over range skips inside gaps", "10-30/5 * * * *", utc(22, 6, 11, time.August, time.Tuesday, 2026), false},
		{"local zone evaluated per wall clock", "30 8 * * *", time.Date(2026, time.August, 26, 8, 30, 0, 0, ResolveTimezone("Asia/Shanghai")), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := ParseCronExpression(tc.expression)
			if err != nil {
				t.Fatalf("parse %q: %v", tc.expression, err)
			}
			if got := spec.Matches(tc.instant); got != tc.want {
				t.Fatalf("expression %q at %s: matched=%v want %v", tc.expression, tc.instant, got, tc.want)
			}
		})
	}
}

func TestParseCronExpressionRejectsInvalid(t *testing.T) {
	for _, expression := range []string{
		"", "   ", "* * * *", "* * * * * *", // field count
		"60 * * * *", "-1 * * * *", "a * * * *", "? * * * *", "* * * * 8",
		"0 24 * * *", "0 0 32 * *", "0 0 * 13 *",
		"*/0 * * * *", "10-5 * * * *", "1-,3 * * * *", ",5 * * * *", "5,,* * * *",
	} {
		if _, err := ParseCronExpression(expression); err == nil {
			t.Fatalf("expression %q must be rejected", expression)
		}
	}
}

func TestNextFireAfterFindsNextMatchingMinute(t *testing.T) {
	spec, err := ParseCronExpression("*/15 * * * *")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, time.August, 26, 10, 7, 12, 0, time.UTC)
	next := NextFireAfter(spec, base)
	if want := base.Truncate(time.Minute).Add(8 * time.Minute); !next.Equal(want) {
		t.Fatalf("next after %s = %s, want %s", base, next, want)
	}
	lastSlot := time.Date(2026, time.August, 26, 10, 45, 0, 0, time.UTC)
	if got := NextFireAfter(spec, lastSlot); !got.Equal(time.Date(2026, time.August, 26, 11, 0, 0, 0, time.UTC)) {
		t.Fatalf("wrap-around next = %s", got)
	}
	daily, err := ParseCronExpression("30 8 * * *")
	if err != nil {
		t.Fatal(err)
	}
	afterRun := time.Date(2026, time.August, 26, 8, 31, 0, 0, time.UTC)
	if got := NextFireAfter(daily, afterRun); !got.Equal(time.Date(2026, time.August, 27, 8, 30, 0, 0, time.UTC)) {
		t.Fatalf("daily next = %s, want next-day 08:30", got)
	}
}

func TestResolveTimezoneFallbacksToUTC(t *testing.T) {
	if got := ResolveTimezone(""); got != time.UTC {
		t.Fatalf("empty name should fall back to UTC, got %v", got)
	}
	if got := ResolveTimezone("Mars/Olympus_Mons"); got != time.UTC {
		t.Fatalf("unknown name should fall back to UTC, got %v", got)
	}
	shanghai := ResolveTimezone("Asia/Shanghai")
	if shanghai == time.UTC || strings.Contains(shanghai.String(), "UTC") {
		t.Fatalf("valid name should resolve, got %v", shanghai)
	}
}

// ---------------------------------------------------------------------------
// Window keys + in-process guard
// ---------------------------------------------------------------------------

func TestScheduleWindowKeyShapeAndUniqueness(t *testing.T) {
	jobID := "8760418e-853f-43f6-b1b8-39cf10104678"
	windowA := time.Unix(1_797_000_000, 0).UTC()
	windowB := windowA.Add(time.Minute)
	keyA := ScheduleWindowKey(jobID, windowA)
	if keyA != ScheduleWindowKey(jobID, windowA.Truncate(time.Second)) {
		t.Fatal("sub-minute components must not change the window key")
	}
	if ScheduleWindowKey(jobID, windowB) == keyA {
		t.Fatal("adjacent windows must get distinct keys")
	}
	if !strings.HasPrefix(keyA, ScheduledRunKeyPrefix+jobID+":") {
		t.Fatalf("unexpected key shape %q", keyA)
	}
	if len(keyA) < 16 {
		t.Fatalf("window key %q is below the CreateRun idempotency length floor", keyA)
	}
}

func TestWindowGuardReservesOnceUntilTTLExpires(t *testing.T) {
	guard := newWindowGuard()
	now := time.Unix(1_797_000_000, 0).UTC()
	if !guard.Reserve("job|minute1", now) {
		t.Fatal("first reservation must succeed")
	}
	if guard.Reserve("job|minute1", now.Add(10*time.Second)) {
		t.Fatal("duplicate reservation inside TTL must be rejected")
	}
	if !guard.Reserve("job|minute2", now.Add(20*time.Second)) {
		t.Fatal("distinct key must reserve")
	}
	later := now.Add(windowGuardTTL + time.Minute)
	if !guard.Reserve("job|minute1", later) {
		t.Fatal("entry older than TTL should be evicted and reservable again")
	}
}

// ---------------------------------------------------------------------------
// Scheduler dedup with injected fake clock and fake persistence
// ---------------------------------------------------------------------------

type fakeScheduleEnqueuer struct {
	jobs         []CronJobRef
	handled      map[string]bool
	enqueueErrAt map[string]error
	failureMarks []string
	fired        []string
}

func newFakeEnqueuer(jobs []CronJobRef) *fakeScheduleEnqueuer {
	return &fakeScheduleEnqueuer{
		jobs:         jobs,
		handled:      map[string]bool{},
		enqueueErrAt: map[string]error{},
	}
}

func (f *fakeScheduleEnqueuer) EnabledCronJobs(ctx context.Context, limit int) ([]CronJobRef, error) {
	return f.jobs, nil
}

func (f *fakeScheduleEnqueuer) ScheduledRunWindowHandled(ctx context.Context, jobID, windowKey string) (bool, error) {
	return f.handled[jobID+"|"+windowKey], nil
}

func (f *fakeScheduleEnqueuer) CreateScheduledRun(ctx context.Context, jobID, windowKey string) error {
	composite := jobID + "|" + windowKey
	if err := f.enqueueErrAt[composite]; err != nil {
		return err
	}
	f.handled[composite] = true
	f.fired = append(f.fired, composite)
	return nil
}

func (f *fakeScheduleEnqueuer) RecordScheduleFailure(ctx context.Context, jobID, windowKey, errorCode, summary string) error {
	composite := jobID + "|" + windowKey
	f.failureMarks = append(f.failureMarks, composite+" code="+errorCode)
	f.handled[composite] = true
	return nil
}

func cronJob(id, expression, timezone string) CronJobRef {
	return CronJobRef{ID: id, Trigger: map[string]any{"type": "cron", "expression": expression}, Timezone: timezone}
}

const quarterHourJobID = "11111111-1111-4111-8111-111111111111"
const quarterHourExpression = "*/15 * * * *"

func matchingMinuteClock() time.Time {
	return time.Date(2026, time.August, 26, 10, 0, 30, 0, time.UTC) // minute :00 hits */15
}

func TestSchedulerTickDeduplicatesPerMinuteWindow(t *testing.T) {
	enqueuer := newFakeEnqueuer([]CronJobRef{cronJob(quarterHourJobID, quarterHourExpression, "UTC")})
	scheduler := NewScheduler(enqueuer, nil)
	clockNow := matchingMinuteClock()
	scheduler.Clock = func() time.Time { return clockNow }

	ctx := context.Background()
	if err := scheduler.Tick(ctx); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	if len(enqueuer.fired) != 1 {
		t.Fatalf("expected one fired run, got %v", enqueuer.fired)
	}
	firstWindow := ScheduleWindowKey(quarterHourJobID, clockNow.In(time.UTC).Truncate(time.Minute))
	if !strings.HasSuffix(enqueuer.fired[0], firstWindow) {
		t.Fatalf("fired composite %q should end with window key %q", enqueuer.fired[0], firstWindow)
	}

	// Same tick minute again: in-process guard must suppress a second run.
	if err := scheduler.Tick(ctx); err != nil {
		t.Fatalf("second tick same minute: %v", err)
	}
	if len(enqueuer.fired) != 1 {
		t.Fatalf("in-process guard failed, fired=%v", enqueuer.fired)
	}

	// A restart drops the in-memory guard; the DB-side existence check must
	// still prevent a duplicate for the same window.
	restarted := NewScheduler(enqueuer, nil)
	restarted.Clock = func() time.Time { return clockNow }
	if err := restarted.Tick(ctx); err != nil {
		t.Fatalf("restarted tick same minute: %v", err)
	}
	if len(enqueuer.fired) != 1 {
		t.Fatalf("DB-side dedup failed, fired=%v", enqueuer.fired)
	}

	// Advance into a non-matching minute: nothing fires.
	clockNow = clockNow.Add(14 * time.Minute) // 10:14:30
	if err := scheduler.Tick(ctx); err != nil {
		t.Fatalf("non-matching tick: %v", err)
	}
	if len(enqueuer.fired) != 1 {
		t.Fatalf("scheduler fired outside its schedule, fired=%v", enqueuer.fired)
	}

	// Advance to the next matching minute: exactly one more run.
	clockNow = matchingMinuteClock().Add(45 * time.Minute) // 10:45:30
	if err := scheduler.Tick(ctx); err != nil {
		t.Fatalf("later tick: %v", err)
	}
	if len(enqueuer.fired) != 2 {
		t.Fatalf("second window should fire once, fired=%v", enqueuer.fired)
	}
}

func TestSchedulerTimezoneAwareDueEvaluation(t *testing.T) {
	jobID := "22222222-2222-4222-8222-222222222222"
	enqueuer := newFakeEnqueuer([]CronJobRef{cronJob(jobID, "30 8 * * *", "Asia/Shanghai")})
	scheduler := NewScheduler(enqueuer, nil)
	// 00:30 UTC == 08:30 Shanghai.
	clockNow := time.Date(2026, time.August, 26, 0, 30, 20, 0, time.UTC)
	scheduler.Clock = func() time.Time { return clockNow }
	if err := scheduler.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(enqueuer.fired) != 1 {
		t.Fatalf("timezone-shifted cron should fire, fired=%v", enqueuer.fired)
	}
}

func TestSchedulerRecordsFailureAndConsumesWindow(t *testing.T) {
	jobID := "33333333-3333-4333-8333-333333333333"
	enqueuer := newFakeEnqueuer([]CronJobRef{cronJob(jobID, quarterHourExpression, "UTC")})
	clockNow := matchingMinuteClock()
	windowKey := ScheduleWindowKey(jobID, clockNow.Truncate(time.Minute))
	enqueuer.enqueueErrAt[jobID+"|"+windowKey] = fmt.Errorf("agent application disabled since creation")
	scheduler := NewScheduler(enqueuer, nil)
	scheduler.Clock = func() time.Time { return clockNow }

	if err := scheduler.Tick(context.Background()); err != nil {
		t.Fatalf("tick with failing enqueue must still succeed at scheduler level: %v", err)
	}
	if len(enqueuer.fired) != 0 {
		t.Fatalf("no run should be created for a failing job, fired=%v", enqueuer.fired)
	}
	if len(enqueuer.failureMarks) != 1 || !strings.Contains(enqueuer.failureMarks[0], "schedule_failed") {
		t.Fatalf("failure must be recorded once with schedule_failed code, marks=%v", enqueuer.failureMarks)
	}

	// The failure marker consumes the window: even a restarted scheduler with
	// an empty in-memory guard does not retry within the same minute.
	restarted := NewScheduler(enqueuer, nil)
	restarted.Clock = func() time.Time { return clockNow }
	if err := restarted.Tick(context.Background()); err != nil {
		t.Fatalf("restarted tick: %v", err)
	}
	if len(enqueuer.failureMarks) != 1 {
		t.Fatalf("failure marker must consume the window, marks=%v", enqueuer.failureMarks)
	}
}

func TestSchedulerSkipsInvalidExpressionsWithoutBlockingOthers(t *testing.T) {
	brokenID := "44444444-4444-4444-8444-444444444444"
	enqueuer := newFakeEnqueuer([]CronJobRef{
		cronJob(brokenID, "not a cron", "UTC"),
		cronJob(quarterHourJobID, quarterHourExpression, "UTC"),
	})
	scheduler := NewScheduler(enqueuer, nil)
	clockNow := matchingMinuteClock()
	scheduler.Clock = func() time.Time { return clockNow }
	if err := scheduler.Tick(context.Background()); err != nil {
		t.Fatalf("invalid expression must not fail the tick: %v", err)
	}
	for _, composite := range enqueuer.fired {
		if strings.HasPrefix(composite, brokenID) {
			t.Fatalf("broken job must not fire, fired=%v", enqueuer.fired)
		}
	}
	if len(enqueuer.fired) != 1 {
		t.Fatalf("healthy job should still fire exactly once, fired=%v", enqueuer.fired)
	}
}

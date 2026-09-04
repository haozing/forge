package worker

import (
	"context"
	"log"

	"agentchunzhi/internal/eventing"
	"agentchunzhi/internal/review"

	"github.com/riverqueue/river"
)

// ScheduledPublicationArgs drives the 1-minute periodic sweep of due
// scheduled publication requests (G4): approved intents whose moment has
// arrived. This is a publishing-domain implementation detail, not a revival
// of the retired automation scheduling framework (docs/产品文档-v2 §21.2):
// no management surface, no cron configuration, no external callbacks.
type ScheduledPublicationArgs struct{}

func (ScheduledPublicationArgs) Kind() string { return "maintenance.scheduled_publication" }

func (ScheduledPublicationArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 3, Queue: eventing.QueueMaintenance}
}

type ScheduledPublicationWorker struct {
	river.WorkerDefaults[ScheduledPublicationArgs]
	Reviews *review.Service
}

func (w *ScheduledPublicationWorker) Work(ctx context.Context, job *river.Job[ScheduledPublicationArgs]) error {
	executed, failed, err := w.Reviews.ExecuteDueScheduled(ctx)
	if err != nil {
		return err
	}
	if executed > 0 || failed > 0 {
		log.Printf("scheduled publication sweep: executed=%d deferred_failed=%d", executed, failed)
	}
	return nil
}

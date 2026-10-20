package reminder

import (
	"context"
	"log"
	"time"
)

// Worker only executes domain reminders. It does not invoke an LLM.
type Worker struct{ store *Store }

func NewWorker(service *Service) *Worker { return &Worker{service.store} }
func (w *Worker) Work(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for i := 0; i < 100 && ctx.Err() == nil; i++ {
				sent, err := w.Deliver(ctx)
				if err != nil {
					log.Printf("reminder delivery failed: %v", err)
					break
				}
				if !sent {
					break
				}
			}
		}
	}
}
func (w *Worker) Deliver(ctx context.Context) (bool, error) { return w.store.Deliver(ctx) }

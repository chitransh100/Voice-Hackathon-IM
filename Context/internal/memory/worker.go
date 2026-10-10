package memory

import (
	"context"
	"log/slog"
	"time"
)

type Worker struct {
	builder  *Builder
	interval time.Duration
	logger   *slog.Logger
}

func NewWorker(builder *Builder, interval time.Duration, logger *slog.Logger) *Worker {
	return &Worker{builder: builder, interval: interval, logger: logger}
}

func (w *Worker) Run(ctx context.Context) {
	w.runOnce(ctx)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.runOnce(ctx)
		}
	}
}

func (w *Worker) runOnce(ctx context.Context) {
	rebuilt, err := w.builder.RebuildStale(ctx)
	if err != nil {
		w.logger.Error("snapshot worker failed", "error", err)
		return
	}
	if rebuilt > 0 {
		w.logger.Info("snapshot worker published contexts", "count", rebuilt)
	}
}

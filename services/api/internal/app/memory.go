package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai/memory"
	"github.com/LizHu95/Jetaime/services/api/internal/config"
)

// BackfillMemory processes pending history and embeddings once, retaining progress.
func BackfillMemory(ctx context.Context, cfg config.Config) error {
	pool, err := openDatabase(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	service, err := cfg.Memory.Build()
	if err != nil {
		return err
	}
	service.Store = memory.NewStore(pool, cfg.Memory)
	if err = service.Store.Initialize(ctx); err != nil {
		return err
	}
	for {
		processed, e := service.ProcessOne(ctx, true)
		if e != nil {
			return e
		}
		if !processed {
			break
		}
	}
	if err = service.Store.RepairQueue(ctx); err != nil {
		return err
	}
	if err = service.DrainEmbeddings(ctx); err != nil {
		return err
	}
	var pending int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM memory_progress p JOIN chat_groups g ON g.id=p.group_id WHERE g.status='active' AND EXISTS(SELECT 1 FROM messages m WHERE m.group_id=p.group_id AND m.seq>p.cursor_seq)`).Scan(&pending); err != nil {
		return err
	}
	if pending > 0 {
		return errors.New("memory backfill has leased or retrying batches; resume later")
	}
	fmt.Println("Memory backfill complete; persisted progress retained")
	return nil
}

// CheckMemory validates configuration compatibility without inference or database writes.
func CheckMemory(ctx context.Context, cfg config.Config) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	pool, err := openDatabase(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if _, err = cfg.Memory.Build(); err != nil {
		return err
	}
	if err = memory.NewStore(pool, cfg.Memory).CheckPipeline(ctx); err != nil {
		return fmt.Errorf("memory configuration incompatible (extraction=%s, pipeline=%s): %w; restore the previous memory configuration or explicitly rebuild and validate the index before switching", cfg.Memory.Extraction.Model, cfg.Memory.Pipeline(), err)
	}
	fmt.Printf("Memory configuration compatible: extraction=%s, pipeline=%s\n", cfg.Memory.Extraction.Model, cfg.Memory.Pipeline())
	return nil
}

package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/LizHu95/Jetaime/services/api/internal/ai/memory"
	"github.com/LizHu95/Jetaime/services/api/internal/config"
)

func backfillMemory(ctx context.Context, cfg config.Config) error {
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

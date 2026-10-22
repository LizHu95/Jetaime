package chat

import "context"

func (s *Service) GetState(ctx context.Context, group, actor string) (State, error) {
	state, err := s.store.State(ctx, group, actor)
	state.AIEnabled = s.aiEnabled
	return state, err
}

func (s *Service) ListMessages(ctx context.Context, group, actor string, after, before int64, limit int) (Page, error) {
	return s.store.Messages(ctx, group, actor, after, before, limit)
}

func (s *Service) GetMessage(ctx context.Context, group, actor, message string) (Message, error) {
	return s.store.Message(ctx, group, actor, message)
}

func (s *Service) MarkRead(ctx context.Context, group, actor string, seq int64) (int64, error) {
	return s.store.Read(ctx, group, actor, seq)
}

func (s *Service) ListRuns(ctx context.Context, group, actor string) ([]Run, error) {
	return s.store.Runs(ctx, group, actor)
}

func (s *Service) RetryRun(ctx context.Context, group, actor, run string) error {
	if !s.aiEnabled {
		return ErrAIUnavailable
	}
	return s.store.Retry(ctx, group, actor, run)
}

func (s *Service) CloseGroup(ctx context.Context, group, actor string) error {
	return s.store.Close(ctx, group, actor)
}

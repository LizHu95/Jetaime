package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/telemetry"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
)

type Service struct {
	Config    Config
	Store     *Store
	Extractor Extractor
	Embedder  Embedder
}

// ProcessOne is also used by the backfill command. Force ignores wake thresholds,
// never progress, leases or completed records.
func (s *Service) ProcessOne(ctx context.Context, force bool) (bool, error) {
	b, err := s.Store.Claim(ctx, force)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	work, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	work, span := telemetry.Start(work, "memory.process", "CHAIN", attribute.String("session.id", b.GroupID), attribute.Int64("memory.from_seq", b.From), attribute.Int64("memory.to_seq", b.To), attribute.Bool("memory.extraction_reused", b.Extracted))
	if data, e := json.Marshal(b); e == nil {
		telemetry.Content(span, "input.value", string(data))
	}
	err = s.process(work, b)
	if data, e := json.Marshal(map[string]any{"fromSeq": b.From, "toSeq": b.To, "succeeded": err == nil}); e == nil {
		telemetry.Content(span, "output.value", string(data))
	}
	telemetry.End(span, err)
	if err != nil {
		save, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stop()
		_ = s.Store.Fail(save, b)
	}
	return true, err
}
func (s *Service) process(ctx context.Context, b Batch) error {
	candidates := b.Candidates
	var err error
	if !b.Extracted {
		candidates = []Candidate{}
		hasNew := false
		for _, m := range b.Messages {
			if m.Seq > b.From {
				hasNew = true
			}
		}
		if hasNew {
			candidates, err = s.Extractor.Extract(ctx, b)
			if err != nil {
				return err
			}
		}
		if err = traceValidateCandidates(ctx, b, candidates); err != nil {
			return err
		}
		if err = s.Store.SaveExtraction(ctx, b, candidates); err != nil {
			return err
		}
	}
	if err = traceValidateCandidates(ctx, b, candidates); err != nil {
		return err
	}
	mutations := append([]Mutation{}, b.Mutations...)
	if len(mutations) > len(candidates) {
		return ErrInvalid
	}
	for _, c := range candidates[len(mutations):] {
		v, e := s.Store.CachedEmbedding(ctx, s.Embedder, b.GroupID, c.Content)
		if e != nil {
			return e
		}
		related, e := s.Store.Related(ctx, b.GroupID, c, v)
		if e != nil {
			return e
		}
		decision := Decision{Action: "ADD", Content: c.Content}
		exact := false
		for _, d := range related {
			if d.Content == c.Content {
				decision = Decision{Action: "NOOP", TargetID: d.ID}
				exact = true
				break
			}
		}
		if len(related) > 0 && !exact {
			decision, e = s.Extractor.Reconcile(ctx, c, related)
			if e != nil {
				return e
			}
		}
		if err = validateDecision(c, decision, related); err != nil {
			return err
		}
		mutations = append(mutations, Mutation{c, decision})
		if err = s.Store.SaveMutations(ctx, b, mutations); err != nil {
			return err
		}
	}
	return s.Store.Complete(ctx, b, mutations)
}
func traceValidateCandidates(ctx context.Context, b Batch, candidates []Candidate) error {
	_, span := telemetry.Start(ctx, "memory.validate_candidates", "CHAIN", attribute.Int("memory.candidate_count", len(candidates)))
	err := validateCandidates(b, candidates)
	telemetry.End(span, err)
	return err
}

func validateCandidates(b Batch, cs []Candidate) error {
	if len(cs) > 20 {
		return fmt.Errorf("%w: candidate_limit", ErrInvalid)
	}
	sources := map[string]Message{}
	subjects := map[string]bool{"shared": true}
	for _, p := range b.Members {
		subjects[p.ID] = true
	}
	for _, m := range b.Messages {
		sources[m.ID] = m
		subjects[m.SenderID] = true
	}
	seen := map[string]bool{}
	for i, c := range cs {
		invalid := func(reason string) error { return fmt.Errorf("%w: candidate[%d] %s", ErrInvalid, i, reason) }
		if !validKind(c.Kind) {
			return invalid("invalid_kind")
		}
		if !subjects[c.Subject] {
			return invalid("invalid_subject")
		}
		if len(c.EventKey) > 200 || strings.TrimSpace(c.EventKey) == "" {
			return invalid("invalid_event_key")
		}
		if strings.TrimSpace(c.Content) == "" || len(c.Content) > 2000 {
			return invalid("invalid_content")
		}
		if len(c.SourceIDs) == 0 || len(c.SourceIDs) > 43 {
			return invalid("invalid_source_count")
		}
		if c.TemporalScope != "enduring" && c.TemporalScope != "temporary" && c.TemporalScope != "event" {
			return invalid("invalid_temporal_scope")
		}
		fresh := false
		for _, id := range c.SourceIDs {
			m, ok := sources[id]
			if !ok {
				return invalid("source_not_in_batch")
			}
			if m.SenderID == "" {
				return invalid("source_not_user")
			}
			fresh = fresh || m.Seq > b.From
		}
		if !fresh {
			return invalid("context_only_sources")
		}
		key := c.Kind + "\n" + c.Subject + "\n" + c.TemporalScope + "\n" + c.EventKey
		if seen[key] {
			return invalid("duplicate_identity")
		}
		seen[key] = true
	}
	return nil
}

func validKind(k string) bool {
	switch k {
	case "preference", "decision", "plan", "fact", "constraint", "event":
		return true
	}
	return false
}
func validateDecision(c Candidate, d Decision, related []Document) error {
	if len(d.Content) > 2000 {
		return ErrInvalid
	}
	if d.Action == "ADD" {
		if d.TargetID != "" {
			return ErrInvalid
		}
		return nil
	}
	if d.Action != "UPDATE" && d.Action != "NOOP" && d.Action != "INVALIDATE" {
		return ErrInvalid
	}
	if _, err := uuid.Parse(d.TargetID); err != nil {
		return ErrInvalid
	}
	if d.Action != "NOOP" && !c.ExplicitChange {
		return ErrInvalid
	}
	for _, old := range related {
		if old.ID == d.TargetID && old.Subject == c.Subject && old.Kind == c.Kind && old.EventKey == c.EventKey && old.TemporalScope == c.TemporalScope {
			return nil
		}
	}
	return ErrInvalid
}
func chunks(messages []Message, from int64) []Document {
	out := []Document{}
	var d Document
	flush := func() {
		if len(d.SourceIDs) > 0 {
			out = append(out, d)
		}
		d = Document{}
	}
	for _, m := range messages {
		if m.Seq <= from || m.SenderID == "" || strings.TrimSpace(m.Text) == "" {
			continue
		}
		text := fmt.Sprintf("[%s] %s (%s): %s\n", m.SentAt.Format(time.RFC3339), m.SenderName, m.SenderID, m.Text)
		if len(d.SourceIDs) > 0 && (len(d.Content)+len(text) > 4500 || len(d.SourceIDs) >= 12 || m.SentAt.Sub(d.End) > 20*time.Minute) {
			flush()
		}
		if len(d.SourceIDs) == 0 {
			d.Start = m.SentAt
		}
		d.Content += text
		d.SourceIDs = append(d.SourceIDs, m.ID)
		d.End = m.SentAt
	}
	flush()
	return out
}
func (s *Service) Work(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	repairAt := time.Time{}
	for {
		if ctx.Err() != nil {
			return
		}
		if time.Now().After(repairAt) {
			if err := s.Store.Initialize(ctx); err != nil {
				log.Print("memory discovery failed")
			}
			if err := s.Store.RepairQueue(ctx); err != nil {
				log.Print("memory embedding repair failed")
			}
			repairAt = time.Now().Add(time.Minute)
		}
		processed, err := s.ProcessOne(ctx, false)
		if err != nil {
			log.Printf("memory processing failed: %v", safeError(err))
		}
		if processed && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func safeError(err error) string {
	switch {
	case errors.Is(err, ErrLease):
		return "lease_lost"
	case errors.Is(err, ErrInvalid):
		return "invalid_model_result"
	default:
		return "processing_failed"
	}
}
func (s *Service) WorkEmbeddings(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		j, err := s.Store.claimEmbedding(ctx, "", "")
		if err == nil {
			_, err = s.Store.runEmbedding(ctx, s.Embedder, j)
			if err == nil {
				continue
			}
		}
		if err != nil && !errors.Is(err, ErrNotFound) {
			log.Print("memory embedding job failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) DrainEmbeddings(ctx context.Context) error {
	for {
		j, err := s.Store.claimEmbedding(ctx, "", "")
		if errors.Is(err, ErrNotFound) {
			break
		}
		if err != nil {
			return err
		}
		if _, err = s.Store.runEmbedding(ctx, s.Embedder, j); err != nil {
			return err
		}
	}
	var pending int
	err := s.Store.Pool.QueryRow(ctx, `SELECT count(*) FROM memory_embeddings e JOIN chat_groups g ON g.id=e.group_id WHERE g.status='active' AND e.model_version=$1 AND e.dimension=$2 AND e.status<>'ready'`, s.Config.ModelVersion(), s.Config.Embedding.Dimension).Scan(&pending)
	if err != nil {
		return err
	}
	if pending > 0 {
		return fmt.Errorf("%d embedding jobs are leased or awaiting retry", pending)
	}
	return nil
}

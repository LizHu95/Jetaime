package debugui

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/telemetry"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type Stage struct {
	Name       string         `json:"name"`
	ID         string         `json:"id"`
	ParentID   string         `json:"parentId"`
	StartedAt  time.Time      `json:"startedAt"`
	DurationMS float64        `json:"durationMs"`
	Error      string         `json:"error,omitempty"`
	Input      any            `json:"input,omitempty"`
	Output     any            `json:"output,omitempty"`
	Attributes map[string]any `json:"attributes"`
}

type Trace struct {
	ID         string    `json:"id"`
	Operation  string    `json:"operation"`
	Provider   string    `json:"provider"`
	Model      string    `json:"model"`
	StartedAt  time.Time `json:"startedAt"`
	DurationMS float64   `json:"durationMs"`
	Error      string    `json:"error,omitempty"`
	Stages     []Stage   `json:"stages"`
}

// 每个操作使用独立同步 recorder；不依赖 Phoenix，也不修改全局 tracer。
type recorder struct {
	mu     sync.Mutex
	stages []Stage
}

func (*recorder) OnStart(context.Context, sdktrace.ReadWriteSpan) {}
func (r *recorder) OnEnd(span sdktrace.ReadOnlySpan) {
	s := Stage{Name: span.Name(), ID: span.SpanContext().SpanID().String(), ParentID: span.Parent().SpanID().String(), StartedAt: span.StartTime(), DurationMS: float64(span.EndTime().Sub(span.StartTime())) / float64(time.Millisecond), Error: span.Status().Description, Attributes: map[string]any{}}
	for _, a := range span.Attributes() {
		key, value := string(a.Key), a.Value.AsInterface()
		if key == "input.value" || key == "output.value" {
			var decoded any
			if raw, ok := value.(string); ok {
				_ = json.Unmarshal([]byte(raw), &decoded)
			}
			if key == "input.value" {
				s.Input = decoded
			} else {
				s.Output = decoded
			}
		} else {
			s.Attributes[key] = value
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stages = append(r.stages, s)
}
func (*recorder) Shutdown(context.Context) error   { return nil }
func (*recorder) ForceFlush(context.Context) error { return nil }
func record(ctx context.Context) (context.Context, *recorder, *sdktrace.TracerProvider) {
	r := &recorder{stages: []Stage{}}
	p := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(r), sdktrace.WithSampler(sdktrace.AlwaysSample()))
	return telemetry.WithTracer(ctx, p.Tracer("jetaime.debugui")), r, p
}
func (r *recorder) snapshot() []Stage {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := append([]Stage{}, r.stages...)
	sort.SliceStable(result, func(i, j int) bool { return result[i].StartedAt.Before(result[j].StartedAt) })
	return result
}

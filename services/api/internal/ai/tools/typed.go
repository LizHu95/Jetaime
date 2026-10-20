package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/telemetry"
	"github.com/google/jsonschema-go/jsonschema"
	"go.opentelemetry.io/otel/attribute"
)

type Handler[I, O any] func(context.Context, Scope, I) (O, error)
type Options[O any] struct {
	Input   func(*jsonschema.Schema)
	Output  func(*jsonschema.Schema)
	Present func(O) Completion
}
type Typed[I, O any] struct {
	metadata      Metadata
	definition    Definition
	input, output *jsonschema.Resolved
	handler       Handler[I, O]
	present       func(O) Completion
	initErr       error
}

// NewTyped derives both schemas from Go types. Constraint callbacks add enums,
// patterns and bounds without duplicating field names/types/requiredness in JSON.
// Invalid contracts are reported by Registry.New, before the application starts.
func NewTyped[I, O any](meta Metadata, handler Handler[I, O], options ...Options[O]) *Typed[I, O] {
	t := &Typed[I, O]{metadata: meta, handler: handler}
	t.initErr = t.initialize(options)
	return t
}

var validName = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,128}$`)

func (t *Typed[I, O]) initialize(options []Options[O]) error {
	if !validName.MatchString(t.metadata.Name) || strings.TrimSpace(t.metadata.Description) == "" || t.handler == nil || len(options) > 1 || t.metadata.Timeout < 0 {
		return fmt.Errorf("invalid tool metadata or handler: %q", t.metadata.Name)
	}
	input, err := jsonschema.For[I](nil)
	if err != nil {
		return fmt.Errorf("tool %s input: %w", t.metadata.Name, err)
	}
	output, err := jsonschema.For[O](nil)
	if err != nil {
		return fmt.Errorf("tool %s output: %w", t.metadata.Name, err)
	}
	if input.Type != "object" || output.Type != "object" {
		return fmt.Errorf("tool %s input and output must be object types", t.metadata.Name)
	}
	if len(options) == 1 {
		o := options[0]
		if o.Input != nil {
			o.Input(input)
		}
		if o.Output != nil {
			o.Output(output)
		}
		t.present = o.Present
	}
	t.input, err = input.Resolve(nil)
	if err != nil {
		return fmt.Errorf("tool %s input schema: %w", t.metadata.Name, err)
	}
	t.output, err = output.Resolve(nil)
	if err != nil {
		return fmt.Errorf("tool %s output schema: %w", t.metadata.Name, err)
	}
	rawIn, err := json.Marshal(input)
	if err != nil {
		return err
	}
	rawOut, err := json.Marshal(output)
	if err != nil {
		return err
	}
	t.definition = Definition{Name: t.metadata.Name, Description: t.metadata.Description, InputSchema: rawIn, OutputSchema: rawOut, Annotations: Annotations{ReadOnly: t.metadata.ReadOnly, Idempotent: t.metadata.Idempotent}}
	return nil
}
func (t *Typed[I, O]) contractError() error { return t.initErr }
func (t *Typed[I, O]) Definition() Definition {
	d := t.definition
	d.InputSchema = append(json.RawMessage(nil), d.InputSchema...)
	d.OutputSchema = append(json.RawMessage(nil), d.OutputSchema...)
	return d
}

var ErrInvalidOutput = errors.New("tool output violates its schema")

type CallError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *CallError) Error() string                { return e.Code + ": " + e.Message }
func (e *CallError) Unwrap() error                { return ai.ErrInvalidDecision }
func InvalidArguments(code, message string) error { return &CallError{Code: code, Message: message} }
func Repairable(err error) (*CallError, bool) {
	var e *CallError
	ok := errors.As(err, &e)
	return e, ok
}

// Invoke is the single execution pipeline, including callers outside Registry.
// Only explicitly repairable argument errors should be sent back to the model.
// A timeout or invalid output is never automatically replayed: writes may have committed.
func (t *Typed[I, O]) Invoke(ctx context.Context, scope Scope, raw json.RawMessage) (result Result, err error) {
	if t.initErr != nil {
		return result, t.initErr
	}
	record := Execution{StartedAt: time.Now(), Kind: t.metadata.Kind, Model: t.metadata.Model, PromptVersion: t.metadata.Version}
	ctx = context.WithValue(ctx, executionKey{}, &record)
	timeout := t.metadata.Timeout
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ctx, span := telemetry.Start(ctx, "ai.tool.execute", "TOOL", attribute.String("tool.name", t.metadata.Name), attribute.String("session.id", scope.Context.GroupID), attribute.Int64("tool.context_end_seq", scope.ContextEndSeq))
	telemetry.Content(span, "langfuse.observation.input", string(raw))
	defer func() {
		record.DurationMS = time.Since(record.StartedAt).Milliseconds()
		result.Execution = record
		var traceErr error
		if err != nil {
			traceErr = errors.New("tool execution failed")
			if e, ok := Repairable(err); ok {
				traceErr = errors.New(e.Code)
			}
		}
		telemetry.End(span, traceErr)
	}()
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if err = ai.ValidateContext(scope.Context); err != nil {
		return result, err
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return result, InvalidArguments("invalid_arguments", "arguments 必须是符合 inputSchema 的 JSON 对象。")
	}
	if e := t.input.Validate(value); e != nil {
		return result, InvalidArguments("invalid_arguments", "参数不符合 inputSchema："+e.Error())
	}
	var input I
	if ai.DecodeJSON(raw, &input) != nil {
		return result, InvalidArguments("invalid_arguments", "参数无法解码为工具输入类型。")
	}
	output, err := t.handler(ctx, scope, input)
	if err != nil {
		return result, err
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	data, e := json.Marshal(output)
	if e != nil {
		return result, fmt.Errorf("%w: cannot encode result", ErrInvalidOutput)
	}
	if json.Unmarshal(data, &value) != nil {
		return result, ErrInvalidOutput
	}
	if e = t.output.Validate(value); e != nil {
		return result, fmt.Errorf("%w: %s", ErrInvalidOutput, t.metadata.Name)
	}
	result.Data = data
	result.Terminal = t.metadata.Terminal
	if t.present != nil {
		result.Completion = t.present(output)
	}
	telemetry.Content(span, "langfuse.observation.output", string(data))
	return result, nil
}

type executionKey struct{}

func RecordStep(ctx context.Context, step ai.Step) {
	if r, ok := ctx.Value(executionKey{}).(*Execution); ok {
		r.Steps = append(r.Steps, step)
	}
}
func RecordModelIdentity(ctx context.Context, identity llm.Identity) {
	if r, ok := ctx.Value(executionKey{}).(*Execution); ok {
		r.ModelIdentity = &identity
	}
}

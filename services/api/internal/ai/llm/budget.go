package llm

import (
	"errors"
	"fmt"
)

var ErrContextLimit = errors.New("model context exceeds capacity")

const requestFramingTokens = 512

// InputTokenCount counts the complete request with the embedded Qwen BPE
// tokenizer. It includes every message, the separately supplied schema, and a
// fixed allowance for provider chat framing that is not represented as text.
func InputTokenCount(in Request) (int, error) {
	in, _ = compactRequest(in)
	count, err := CountQwenTokens(string(in.Format))
	if err != nil {
		return 0, err
	}
	count += requestFramingTokens
	for _, message := range in.Messages {
		messageTokens, err := CountQwenTokens(message.Role + "\n" + message.Content)
		if err != nil {
			return 0, err
		}
		count += messageTokens + 32
	}
	return count, nil
}

// InputTokenUpperBound is retained for diagnostics that cannot return an
// error. It now reports the Qwen-token count, not a UTF-8 byte estimate.
// Admission paths must use CheckContextBudget, which propagates tokenizer
// initialization errors instead of silently accepting a request.
func InputTokenUpperBound(in Request) int {
	count, err := InputTokenCount(in)
	if err != nil {
		return 0
	}
	return count
}

// CheckContextBudget rejects locally before any provider I/O. A request may
// lower the configured capacity, but cannot raise it or omit output reservation.
func CheckContextBudget(in Request, configuredLimit int) error {
	if configuredLimit <= 0 || in.Options.NumCtx <= 0 || in.Options.NumPredict <= 0 {
		return fmt.Errorf("%w: positive context and output limits are required", ErrContextLimit)
	}
	limit := min(configuredLimit, in.Options.NumCtx)
	input, err := InputTokenCount(in)
	if err != nil {
		return fmt.Errorf("%w: tokenize request: %v", ErrContextLimit, err)
	}
	output := in.Options.NumPredict
	// Subtraction avoids overflow from a maliciously large output reservation.
	if output > limit || input > limit-output {
		return fmt.Errorf("%w: input tokens=%d, output reserve=%d, context limit=%d", ErrContextLimit, input, output, limit)
	}
	return nil
}

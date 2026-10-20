package llm

import (
	"errors"
	"fmt"
)

var ErrContextLimit = errors.New("model context exceeds capacity")

// InputTokenUpperBound is a conservative UTF-8 byte estimate, not an exact
// model tokenizer. Include the separately supplied schema and framing as well
// as every message, even when the schema is also present in the system prompt.
func InputTokenUpperBound(in Request) int {
	bound := len(in.Format) + 512
	for _, message := range in.Messages {
		bound += len(message.Role) + len(message.Content) + 32
	}
	return bound
}

// CheckContextBudget rejects locally before any provider I/O. A request may
// lower the configured capacity, but cannot raise it or omit output reservation.
func CheckContextBudget(in Request, configuredLimit int) error {
	if configuredLimit <= 0 || in.Options.NumCtx <= 0 || in.Options.NumPredict <= 0 {
		return fmt.Errorf("%w: positive context and output limits are required", ErrContextLimit)
	}
	limit := min(configuredLimit, in.Options.NumCtx)
	input := InputTokenUpperBound(in)
	output := in.Options.NumPredict
	// Subtraction avoids overflow from a maliciously large output reservation.
	if output > limit || input > limit-output {
		return fmt.Errorf("%w: input byte estimate=%d, output reserve=%d, context limit=%d", ErrContextLimit, input, output, limit)
	}
	return nil
}

package tools

import (
	"bytes"
	"encoding/json"
	"io"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
)

// DecodeWithEnvelope accepts the ordinary call protocol first. A bare argument
// object can only acquire an envelope if exactly one currently visible tool's
// complete input contract accepts it. It never fills or changes argument values,
// executes a tool, or substitutes for the execution pipeline's authorization and
// evidence checks. Partial envelopes and ambiguous objects require model repair.
func (r *Registry) DecodeWithEnvelope(content string, visible []Definition) (Call, bool, error) {
	call, err := r.Decode(content)
	if err == nil {
		return call, false, nil
	}
	raw := json.RawMessage(bytes.TrimSpace([]byte(content)))
	var fields map[string]json.RawMessage
	if ai.DecodeJSON(raw, &fields) != nil || len(fields) == 0 || !uniqueJSONKeys(raw) {
		return Call{}, false, err
	}
	if _, exists := fields["name"]; exists {
		return Call{}, false, err
	}
	if _, exists := fields["arguments"]; exists {
		return Call{}, false, err
	}
	matched := ""
	seen := map[string]bool{}
	for _, definition := range visible {
		if seen[definition.Name] {
			continue
		}
		seen[definition.Name] = true
		tool := r.entries[definition.Name]
		if tool == nil || !tool.acceptsArguments(raw) {
			continue
		}
		if matched != "" {
			return Call{}, false, err
		}
		matched = definition.Name
	}
	if matched == "" {
		return Call{}, false, err
	}
	return Call{Name: matched, Arguments: raw}, true, nil
}

// Duplicate fields, including nested fields, are not eligible for inference.
// The original strict call decoder's behavior remains unchanged.
func uniqueJSONKeys(raw []byte) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value func(int) bool
	value = func(depth int) bool {
		if depth > 64 {
			return false
		}
		token, err := d.Token()
		if err != nil {
			return false
		}
		open, nested := token.(json.Delim)
		if !nested {
			return true
		}
		switch open {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				name, ok := key.(string)
				if err != nil || !ok || seen[name] || !value(depth+1) {
					return false
				}
				seen[name] = true
			}
		case '[':
			for d.More() {
				if !value(depth + 1) {
					return false
				}
			}
		default:
			return false
		}
		close, err := d.Token()
		return err == nil && (open == '{' && close == json.Delim('}') || open == '[' && close == json.Delim(']'))
	}
	if !value(0) {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}

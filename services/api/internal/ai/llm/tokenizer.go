package llm

import (
	_ "embed"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/pkoukk/tiktoken-go"
)

// qwenTokenizerBPE is the official Qwen BPE merge/rank asset. It is embedded
// so context admission is deterministic and does not depend on a network
// service or a Python runtime at deployment time.
//
//go:embed assets/qwen.tiktoken
var qwenTokenizerBPE []byte

var (
	qwenTokenizerOnce sync.Once
	qwenTokenizer     *tiktoken.Tiktoken
	qwenTokenizerErr  error
)

func loadQwenTokenizer() (*tiktoken.Tiktoken, error) {
	qwenTokenizerOnce.Do(func() {
		ranks := make(map[string]int, 151643)
		for _, line := range strings.Split(string(qwenTokenizerBPE), "\n") {
			if line == "" {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) != 2 {
				qwenTokenizerErr = fmt.Errorf("invalid embedded Qwen tokenizer entry")
				return
			}
			piece, err := base64.StdEncoding.DecodeString(fields[0])
			if err != nil {
				qwenTokenizerErr = fmt.Errorf("decode embedded Qwen tokenizer: %w", err)
				return
			}
			rank, err := strconv.Atoi(fields[1])
			if err != nil {
				qwenTokenizerErr = fmt.Errorf("parse embedded Qwen tokenizer rank: %w", err)
				return
			}
			ranks[string(piece)] = rank
		}
		special := map[string]int{"<|endoftext|>": 151643, "<|im_start|>": 151644, "<|im_end|>": 151645}
		for i := 0; i < 205; i++ {
			special[fmt.Sprintf("<|extra_%d|>", i)] = 151646 + i
		}
		bpe, err := tiktoken.NewCoreBPE(ranks, special, `(?i:'s|'t|'re|'ve|'m|'ll|'d)|[^\r\n\p{L}\p{N}]?\p{L}+|\p{N}| ?[^\s\p{L}\p{N}]+[\r\n]*|\s*[\r\n]+|\s+(?!\S)|\s+`)
		if err != nil {
			qwenTokenizerErr = fmt.Errorf("initialize embedded Qwen tokenizer: %w", err)
			return
		}
		qwenTokenizer = tiktoken.NewTiktoken(bpe, &tiktoken.Encoding{Name: "qwen", PatStr: "", MergeableRanks: ranks, SpecialTokens: special}, map[string]any{})
	})
	return qwenTokenizer, qwenTokenizerErr
}

// CountQwenTokens returns the token count produced by the embedded official
// Qwen BPE vocabulary. The chat envelope and JSON schema framing are counted
// separately by InputTokenCount because they are not text content.
func CountQwenTokens(text string) (int, error) {
	if text == "" {
		return 0, nil
	}
	tokenizer, err := loadQwenTokenizer()
	if err != nil {
		return 0, err
	}
	return len(tokenizer.EncodeOrdinary(text)), nil
}

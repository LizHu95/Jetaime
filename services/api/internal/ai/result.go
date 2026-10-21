package ai

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

func ResultFromRun(run Run) (Result, string, error) {
	if err := ValidateCards(run.Cards); err != nil {
		return Result{}, "", err
	}
	result := Result{Kind: run.Kind, PromptVersion: run.PromptVersion, Answer: run.Answer, Suggestions: run.Suggestions, HistoryScope: run.HistoryScope, Route: run.Route, Basis: run.Basis, Cards: run.Cards}
	for _, suggestion := range run.Suggestions {
		if strings.ContainsRune(suggestion.Title, '\x00') || strings.ContainsRune(suggestion.TimeExpression, '\x00') {
			return Result{}, "", fmt.Errorf("invalid agent suggestion")
		}
	}
	text := ""
	if run.Answer != nil {
		text = run.Answer.Answer
		if run.HistoryScope != nil && run.HistoryScope.EarlierMessagesOmitted && !run.HistoryScope.HistoricalSearch {
			text += "\n\n本次仅参考近期聊天，未检索更早记录。"
		}
	} else if run.Kind == "extract" {
		if len(run.Suggestions) == 0 {
			text = "当前对话中没有发现需要确认的新待办。"
		} else {
			var lines []string
			lines = append(lines, "从本群对话中整理出以下待办建议（尚未创建）：")
			for i, suggestion := range run.Suggestions {
				line := fmt.Sprintf("%d. %s", i+1, suggestion.Title)
				if suggestion.TimeExpression != "" {
					line += " · " + suggestion.TimeExpression
				}
				lines = append(lines, line)
			}
			text = strings.Join(lines, "\n")
		}
	}
	if !utf8.ValidString(text) || strings.TrimSpace(text) == "" || strings.ContainsRune(text, '\x00') || len(text) > 32000 {
		return Result{}, "", fmt.Errorf("invalid agent reply")
	}
	return result, text, nil
}

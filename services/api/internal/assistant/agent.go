package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

var ErrBudget = errors.New("agent context exceeds capacity")

// Agent exposes the existing workflow against an immutable, authorized snapshot.
// Every execution owns its workflow state, so different groups do not share a
// global lock, history, suggestions or experimental review cache.
type Agent struct {
	model  ChatModel
	name   string
	tokens int
}

func NewAgent(model ChatModel, name string, tokens int) (*Agent, error) {
	if model == nil || strings.TrimSpace(name) == "" || tokens < 4096 {
		return nil, fmt.Errorf("agent requires model and context >=4096")
	}
	return &Agent{model: model, name: name, tokens: tokens}, nil
}
func (a *Agent) Execute(ctx context.Context, input Context, kind, question string) (Run, error) {
	if kind == "" {
		kind = "qa"
	}
	if err := validateSnapshot(input); err != nil {
		return Run{}, err
	}
	w := &Workflow{ChatModel: a.model, DefaultModel: a.name, Tokens: a.tokens, runs: []Run{}, dismissed: map[string]bool{}}
	return w.execute(ctx, input, kind, question, a.name)
}
func validateSnapshot(c Context) error {
	if c.GroupID == "" || c.ActorID == "" {
		return fmt.Errorf("agent requires group and actor")
	}
	fixture := Fixture{Users: c.Users, Groups: []Group{{ID: c.GroupID, Name: c.GroupID}}, Members: []GroupMember{}, Messages: c.Messages}
	actor := false
	for _, user := range c.Users {
		fixture.Members = append(fixture.Members, GroupMember{GroupID: c.GroupID, UserID: user.ID})
		if user.ID == c.ActorID {
			actor = true
		}
	}
	if !actor {
		return fmt.Errorf("actor is not in the snapshot")
	}
	if err := validate(fixture, diskState{Tasks: c.Tasks}); err != nil {
		return err
	}
	for _, message := range c.Messages {
		if message.GroupID != c.GroupID {
			return fmt.Errorf("cross-group snapshot")
		}
	}
	for _, task := range c.Tasks {
		if task.GroupID != c.GroupID {
			return fmt.Errorf("cross-group task")
		}
	}
	return nil
}

// Result is the bounded, durable user-facing part of a workflow run. Internal
// model requests and raw provider responses stay out of message API responses.
type Result struct {
	Kind          string       `json:"kind"`
	PromptVersion string       `json:"promptVersion"`
	Answer        *Answer      `json:"answer,omitempty"`
	Suggestions   []Suggestion `json:"suggestions,omitempty"`
}

func ResultFromRun(run Run) (Result, string, error) {
	result := Result{Kind: run.Kind, PromptVersion: run.PromptVersion, Answer: run.Answer, Suggestions: run.Suggestions}
	for _, suggestion := range run.Suggestions {
		if strings.ContainsRune(suggestion.Title, '\x00') || strings.ContainsRune(suggestion.TimeExpression, '\x00') {
			return Result{}, "", fmt.Errorf("invalid agent suggestion")
		}
	}
	text := ""
	if run.Answer != nil {
		text = run.Answer.Answer
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

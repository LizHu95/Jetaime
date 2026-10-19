package debugconsole

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
)

type Workflow struct {
	Store        *Store
	Executor     ai.Executor
	DefaultModel string
	LogPath      string
	opMu         sync.Mutex
	mu           sync.RWMutex
	runs         []ai.Run
	dismissed    map[string]bool
}

func NewWorkflow(store *Store, executor ai.Executor, name, logPath string) (*Workflow, error) {
	if store == nil || executor == nil {
		return nil, fmt.Errorf("console requires store and executor")
	}
	return &Workflow{Store: store, Executor: executor, DefaultModel: name, LogPath: logPath, runs: []ai.Run{}, dismissed: map[string]bool{}}, nil
}
func (w *Workflow) Runs(group, actor string) ([]ai.Run, error) {
	if _, err := w.Store.Context(group, actor); err != nil {
		return nil, err
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	runs := []ai.Run{}
	for i := len(w.runs) - 1; i >= 0; i-- {
		if w.runs[i].GroupID == group {
			runs = append(runs, w.runs[i])
		}
	}
	return runs, nil
}
func (w *Workflow) record(run *ai.Run) {
	if w.LogPath != "" {
		err := os.MkdirAll(filepath.Dir(w.LogPath), 0700)
		if err == nil {
			var f *os.File
			f, err = os.OpenFile(w.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
			if err == nil {
				b, e := json.Marshal(run)
				if e == nil {
					_, e = f.Write(append(b, '\n'))
				}
				closeErr := f.Close()
				err = e
				if err == nil {
					err = closeErr
				}
			}
		}
		if err != nil {
			run.Warning = "运行结果已保留，但日志写入失败：" + err.Error()
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.runs = append(w.runs, *run)
	if len(w.runs) > 100 {
		w.runs = w.runs[len(w.runs)-100:]
	}
}
func (w *Workflow) Execute(ctx context.Context, group, actor, kind, question, model string) (run ai.Run, err error) {
	if !w.opMu.TryLock() {
		return run, fmt.Errorf("已有 Workflow 正在执行，请稍后再试")
	}
	defer w.opMu.Unlock()
	input, err := w.Store.Context(group, actor)
	if err != nil {
		return run, err
	}
	if model != "" && model != w.DefaultModel {
		return run, fmt.Errorf("model is fixed at startup; restart to switch")
	}
	run, err = w.Executor.Execute(ctx, ai.Invocation{AgentID: "main", Context: input, Mode: kind, Question: question, LoadExtraction: func(context.Context) (ai.Context, error) { return input, nil }})
	if run.ID != "" {
		w.record(&run)
	}
	return run, err
}

func (w *Workflow) Suggestion(group, actor, key string) (ai.Suggestion, error) {
	if _, err := w.Store.Context(group, actor); err != nil {
		return ai.Suggestion{}, err
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.dismissed[key] {
		return ai.Suggestion{}, fmt.Errorf("suggestion was dismissed")
	}
	for _, r := range w.runs {
		if r.Status == "succeeded" && r.GroupID == group {
			for _, s := range r.Suggestions {
				if s.ID == key {
					return s, nil
				}
			}
		}
	}
	return ai.Suggestion{}, fmt.Errorf("suggestion not found (runs are in-memory; re-extract after restart)")
}
func (w *Workflow) Dismiss(group, actor, key string) error {
	if _, err := w.Suggestion(group, actor, key); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.dismissed[key] = true
	return nil
}
func sameSources(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]bool{}
	for _, id := range a {
		m[id] = true
	}
	for _, id := range b {
		if !m[id] {
			return false
		}
	}
	return true
}

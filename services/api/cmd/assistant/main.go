package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/LizHu95/Jetaime/services/api/internal/assistant"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8081", "loopback listen address")
	fixture := flag.String("fixture", "../../local/data/chat-history/history.fixture.json", "editable history fixture")
	state := flag.String("state", "../../local/data/assistant/state.json", "persisted messages and tasks")
	logs := flag.String("log", "../../local/data/assistant/runs.jsonl", "workflow trace log")
	base := flag.String("ollama-url", "http://127.0.0.1:11434", "Ollama base URL")
	model := flag.String("model", "qwen3.5:9b", "model name")
	timeout := flag.Duration("model-timeout", 5*time.Minute, "model timeout")
	tokens := flag.Int("context-tokens", 98304, "context capacity")
	flag.Parse()
	if flag.NArg() != 0 {
		log.Fatal("unexpected arguments")
	}
	host, _, err := net.SplitHostPort(*addr)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		log.Fatal("addr must use a loopback IP")
	}
	paths := []string{*fixture, *state, *logs}
	for i, p := range paths {
		paths[i], err = filepath.Abs(p)
		if err != nil {
			log.Fatal(err)
		}
	}
	if paths[0] == paths[1] || paths[0] == paths[2] || paths[1] == paths[2] {
		log.Fatal("fixture, state and log must be different files")
	}
	store, err := assistant.OpenStore(paths[0], paths[1])
	if err != nil {
		log.Fatal(err)
	}
	provider, err := assistant.NewOllama(*base, *timeout)
	if err != nil {
		log.Fatal(err)
	}
	workflow, err := assistant.NewWorkflow(store, provider, *model, *tokens, paths[2])
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{Handler: assistant.NewServer(workflow), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	fmt.Printf("V2 demo: http://%s\nHistory: %s\nState: %s\nTraces: %s\nLocal simulated identities; no login.\n", listener.Addr(), paths[0], paths[1], paths[2])
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Print(err)
			stop()
		}
	}()
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil {
		_ = server.Close()
	}
}

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/debugui"
	"github.com/LizHu95/Jetaime/services/api/internal/telemetry"
)

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	addr := flag.String("addr", "127.0.0.1:8080", "本机监听地址")
	fixture := flag.String("fixture", "", "可选的本地 fixture JSON")
	ollamaURL := flag.String("ollama-url", "http://localhost:11434", "Ollama 地址")
	model := flag.String("model", "qwen3.5:9b", "默认模型")
	timeout := flag.Duration("model-timeout", 3*time.Minute, "模型请求超时")
	tokens := flag.Int("context-tokens", 8192, "上下文 token 数")
	traceEnabled := flag.Bool("trace", false, "向 Phoenix 上报完整调试输入输出")
	traceEndpoint := flag.String("trace-endpoint", "http://127.0.0.1:6006/v1/traces", "Phoenix OTLP HTTP 地址")
	traceProject := flag.String("trace-project", "jetaime", "Phoenix 项目名")
	flag.Parse()
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	host, _, err := net.SplitHostPort(*addr)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("调试服务只能监听本机 IP，例如 127.0.0.1:8080")
	}
	app, err := debugui.New(debugui.Config{FixturePath: *fixture, OllamaURL: *ollamaURL, Model: *model, ModelTimeout: *timeout, ContextTokens: *tokens, Trace: telemetry.Config{Enabled: *traceEnabled, Endpoint: *traceEndpoint, Project: *traceProject, Warnings: os.Stderr}})
	if err != nil {
		return err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := app.Shutdown(ctx); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "trace: 退出时未能导出全部追踪")
		}
	}()
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: app, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	fmt.Printf("本地调试台：http://%s\n页面会话与 Trace 保存在当前进程内。\n", listener.Addr())
	if *traceEnabled {
		fmt.Printf("Phoenix 上报已开启：%s（项目 %s）\n", *traceEndpoint, *traceProject)
	}
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
			return err
		}
		return nil
	}
}

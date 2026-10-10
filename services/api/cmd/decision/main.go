package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/decisions"
	"github.com/LizHu95/Jetaime/services/api/internal/demo"
	"github.com/LizHu95/Jetaime/services/api/internal/ollama"
	"github.com/LizHu95/Jetaime/services/api/internal/telemetry"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdin, os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
	flags := flag.NewFlagSet("decision", flag.ContinueOnError)
	flags.SetOutput(out)
	name := flags.String("scenario", "normal", "默认场景名称；用 -list 查看")
	list := flags.Bool("list", false, "列出默认场景")
	once := flags.Bool("once", false, "只生成一批")
	interactive := flags.Bool("interactive", false, "交互执行采纳、拒绝、换批和新需求")
	jsonOutput := flags.Bool("json", false, "按 JSON 输出每一步")
	fixturePath := flags.String("fixture", "", "读取人工定义的本地 JSON fixture；省略则用默认虚构资料")
	dump := flags.Bool("dump-fixture", false, "输出完整 fixture JSON")
	providerName := flags.String("provider", "ollama", "推荐生成实现：ollama（后续可扩展云模型）")
	ollamaURL := flags.String("ollama-url", "http://localhost:11434", "Ollama 服务地址")
	model := flags.String("model", "qwen3.5:9b", "Ollama 已下载的模型标签")
	modelTimeout := flags.Duration("model-timeout", 3*time.Minute, "单次模型调用超时，包含加载时间")
	contextTokens := flags.Int("context-tokens", 8192, "Ollama 上下文窗口 token 数")
	traceEnabled := flags.Bool("trace", false, "向本地 Phoenix 记录完整调试输入输出")
	traceEndpoint := flags.String("trace-endpoint", "http://127.0.0.1:6006/v1/traces", "完整 OTLP HTTP 追踪上报地址")
	traceProject := flags.String("trace-project", "jetaime", "Phoenix 项目名称")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if *interactive && *jsonOutput {
		return fmt.Errorf("interactive and json modes cannot be combined")
	}
	fixture := demo.NewFixture()
	if *fixturePath != "" {
		loaded, err := loadFixture(*fixturePath)
		if err != nil {
			return err
		}
		fixture = loaded
	}
	if *dump {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(fixture)
	}
	if *list {
		for _, scenario := range fixture.Scenarios {
			if _, err := fmt.Fprintf(out, "%s: %s（预期 %s）\n", scenario.Name, scenario.Description, scenario.Expected); err != nil {
				return err
			}
		}
		return nil
	}
	scenario, err := fixture.Scenario(*name)
	if err != nil {
		return err
	}
	ctx, shutdown, err := telemetry.Init(ctx, telemetry.Config{Enabled: *traceEnabled, Endpoint: *traceEndpoint, Project: *traceProject, Warnings: os.Stderr})
	if err != nil {
		return err
	}
	defer func() {
		// 即使业务 context 已取消，仍给最后的失败记录独立的导出时间。
		flushCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := shutdown(flushCtx); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "trace: 退出时追踪未全部保存；业务结果不受影响")
		}
	}()
	store, err := decisions.NewMemoryStore(fixture.Dataset)
	if err != nil {
		return err
	}
	// 在这里选择具体生成实现；服务层通过 Provider 接口调用，不依赖 Ollama。
	var provider decisions.Provider
	switch *providerName {
	case "ollama":
		provider, err = ollama.NewProvider(ollama.Config{BaseURL: *ollamaURL, Model: *model, Timeout: *modelTimeout, ContextTokens: *contextTokens})
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported provider %q; use ollama", *providerName)
	}
	service, err := newFixtureService(store, fixture, provider)
	if err != nil {
		return err
	}
	current, err := service.Generate(ctx, scenario.Request, scenario.Request.RequesterID)
	if err != nil {
		return err
	}
	display := func(step string, d decisions.Decision) error {
		if *jsonOutput {
			return json.NewEncoder(out).Encode(struct {
				Step     string             `json:"step"`
				Decision decisions.Decision `json:"decision"`
			}{step, d})
		}
		if _, err := fmt.Fprintf(out, "\n[%s] %s\n%s\nDecision: %s\nSession: %s\n", step, d.Result.Outcome, d.Result.Explanation, d.ID, d.SessionID); err != nil {
			return err
		}
		for i, option := range d.Result.Options {
			if _, err := fmt.Fprintf(out, "%d. %s [%s]\n   %s\n", i+1, option.Title, option.Selection.NoteID, option.Reason); err != nil {
				return err
			}
			for _, match := range option.ParticipantMatches {
				if _, err := fmt.Fprintf(out, "   %s: %s\n", match.UserID, match.Explanation); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := display("生成", current); err != nil {
		return err
	}
	if *interactive {
		// 新资料与新 Checker 在独立 Store 上试运行，成功后才切换；失败保留旧状态。
		reload := func(ctx context.Context, request decisions.Request) (*decisions.DecisionService, decisions.Decision, error) {
			if *fixturePath == "" {
				return nil, decisions.Decision{}, fmt.Errorf("reload 需要启动时指定 -fixture 文件路径")
			}
			loaded, err := loadFixture(*fixturePath)
			if err != nil {
				return nil, decisions.Decision{}, err
			}
			updated, err := store.WithDataset(loaded.Dataset, time.Now())
			if err != nil {
				return nil, decisions.Decision{}, fmt.Errorf("资料校验失败：%w", err)
			}
			nextService, err := newFixtureService(updated, loaded, provider)
			if err != nil {
				return nil, decisions.Decision{}, err
			}
			request.SessionID = ""
			generated, err := nextService.Generate(ctx, request, request.RequesterID)
			if err != nil {
				return nil, decisions.Decision{}, err
			}
			store = updated
			return nextService, generated, nil
		}
		return interact(ctx, service, scenario.Request, current, in, out, display, reload)
	}
	if *once || len(current.Result.Options) == 0 {
		return nil
	}
	// A scripted demo exercises partner feedback, exclusions and a new request.
	if _, err := service.Feedback(ctx, decisions.Feedback{ID: "demo-adopt", DecisionID: current.ID, Action: decisions.FeedbackAdopt, OptionID: current.Result.Options[0].OptionID}, scenario.Request.RequesterID); err != nil {
		return err
	}
	partner := scenario.Request.RequesterID
	if scenario.Request.SpaceID == demo.CoupleSpace {
		partner = demo.UserB
	}
	if len(current.Result.Options) > 1 {
		if _, err := service.Feedback(ctx, decisions.Feedback{ID: "demo-reject", DecisionID: current.ID, Action: decisions.FeedbackReject, OptionID: current.Result.Options[1].OptionID}, partner); err != nil {
			return err
		}
	}
	history, err := service.GetDecision(current.ID, scenario.Request.RequesterID)
	if err != nil {
		return err
	}
	if err := display("采纳与拒绝已记录", history); err != nil {
		return err
	}
	next, err := service.ChangeBatch(ctx, current.ID, partner, "demo-batch")
	if err != nil {
		return err
	}
	if err := display("换一批", next); err != nil {
		return err
	}
	repeated, err := service.ChangeBatch(ctx, current.ID, partner, "demo-batch")
	if err != nil {
		return err
	}
	if repeated.ID != next.ID {
		return fmt.Errorf("batch retry generated a duplicate decision")
	}
	session, err := service.GetSession(current.SessionID, scenario.Request.RequesterID)
	if err != nil {
		return err
	}
	if *jsonOutput {
		if err := json.NewEncoder(out).Encode(struct {
			Step    string            `json:"step"`
			Session decisions.Session `json:"session"`
		}{"Session 与幂等换批", session}); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintf(out, "换批重试返回同一 Decision；Session 排除：%v；当前采纳：%+v\n", session.ExcludedNoteIDs, session.AdoptedOption); err != nil {
			return err
		}
	}
	changed := scenario.Request
	changed.Query = "新的需求：" + changed.Query
	changed.SessionID = ""
	fresh, err := service.Generate(ctx, changed, changed.RequesterID)
	if err != nil {
		return err
	}
	return display("新需求开启新 Session", fresh)
}

// reloadFixture 返回试运行成功的新服务与决策，错误时调用方保留旧引用。
type reloadFixture func(context.Context, decisions.Request) (*decisions.DecisionService, decisions.Decision, error)

func interact(ctx context.Context, service *decisions.DecisionService, request decisions.Request, current decisions.Decision, in io.Reader, out io.Writer, display func(string, decisions.Decision) error, reload reloadFixture) error {
	if _, err := fmt.Fprintln(out, "命令：adopt N [user-a|user-b] / reject N [用户] / batch [用户] / retry / budget 元整数 / query 新需求 / reload / history [DecisionID] / session / quit。全部状态仅在本进程内保留。"); err != nil {
		return err
	}
	scanner := bufio.NewScanner(in)
	sequence := 0
	pendingDecision, pendingActor, pendingID := "", "", ""
	report := func(err error) error {
		_, writeErr := fmt.Fprintf(out, "操作失败，原结果保留：%v\n", err)
		return writeErr
	}
	for {
		if _, err := fmt.Fprint(out, "> "); err != nil {
			return err
		}
		if !scanner.Scan() {
			return scanner.Err()
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 {
			continue
		}
		sequence++
		eventID := fmt.Sprintf("cli-event-%d", sequence)
		actor := request.RequesterID
		switch fields[0] {
		case "quit", "exit":
			return nil
		case "reload":
			if len(fields) != 1 {
				if err := report(fmt.Errorf("用法：reload（重新读取启动时指定的 fixture 文件）")); err != nil {
					return err
				}
				continue
			}
			nextService, generated, err := reload(ctx, request)
			if err != nil {
				if err := report(err); err != nil {
					return err
				}
				continue
			}
			service, current = nextService, generated
			request.SessionID = ""
			pendingDecision, pendingActor, pendingID = "", "", ""
			if err := display("资料已重载，新 Session", current); err != nil {
				return err
			}
		case "adopt", "reject":
			if len(fields) < 2 || len(fields) > 3 {
				if err := report(fmt.Errorf("用法：%s N [用户]", fields[0])); err != nil {
					return err
				}
				continue
			}
			index, err := strconv.Atoi(fields[1])
			if err != nil || index < 1 || index > len(current.Result.Options) {
				if err := report(fmt.Errorf("选项编号无效")); err != nil {
					return err
				}
				continue
			}
			if len(fields) == 3 {
				actor = fields[2]
			}
			action := decisions.FeedbackAdopt
			if fields[0] == "reject" {
				action = decisions.FeedbackReject
			}
			session, err := service.Feedback(ctx, decisions.Feedback{ID: eventID, DecisionID: current.ID, Action: action, OptionID: current.Result.Options[index-1].OptionID}, actor)
			if err != nil {
				if err := report(err); err != nil {
					return err
				}
				continue
			}
			if _, err := fmt.Fprintf(out, "反馈成功；采纳：%+v；排除：%v\n", session.AdoptedOption, session.ExcludedNoteIDs); err != nil {
				return err
			}
		case "batch", "retry":
			if fields[0] == "batch" {
				if len(fields) > 2 {
					if err := report(fmt.Errorf("用法：batch [用户]")); err != nil {
						return err
					}
					continue
				}
				if len(fields) == 2 {
					actor = fields[1]
				}
				pendingDecision, pendingActor, pendingID = current.ID, actor, eventID
			} else if pendingID == "" {
				if err := report(fmt.Errorf("没有可重试的换批命令")); err != nil {
					return err
				}
				continue
			}
			next, err := service.ChangeBatch(ctx, pendingDecision, pendingActor, pendingID)
			if err != nil {
				if err := report(err); err != nil {
					return err
				}
				continue
			}
			current = next
			if err := display("换一批", current); err != nil {
				return err
			}
		case "budget", "query":
			next := request
			next.SessionID = ""
			if fields[0] == "budget" {
				if len(fields) != 2 {
					if err := report(fmt.Errorf("用法：budget 元整数")); err != nil {
						return err
					}
					continue
				}
				yuan, err := strconv.ParseInt(fields[1], 10, 64)
				if err != nil || yuan < 0 || yuan > (1<<63-1)/100 {
					if err := report(fmt.Errorf("预算必须是有效非负整数元")); err != nil {
						return err
					}
					continue
				}
				cents := yuan * 100
				next.Conditions.BudgetMaxCents = &cents
			} else {
				next.Query = strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "query"))
			}
			generated, err := service.Generate(ctx, next, next.RequesterID)
			if err != nil {
				if err := report(err); err != nil {
					return err
				}
				continue
			}
			request, current = next, generated
			pendingID = ""
			if err := display("新 Session", current); err != nil {
				return err
			}
		case "history":
			if len(fields) > 2 {
				if err := report(fmt.Errorf("用法：history [DecisionID]")); err != nil {
					return err
				}
				continue
			}
			id := current.ID
			if len(fields) == 2 {
				id = fields[1]
			}
			history, err := service.GetDecision(id, request.RequesterID)
			if err != nil {
				if err := report(err); err != nil {
					return err
				}
				continue
			}
			if err := json.NewEncoder(out).Encode(history); err != nil {
				return err
			}
		case "session":
			session, err := service.GetSession(current.SessionID, request.RequesterID)
			if err != nil {
				if err := report(err); err != nil {
					return err
				}
				continue
			}
			if err := json.NewEncoder(out).Encode(session); err != nil {
				return err
			}
		default:
			if err := report(fmt.Errorf("未知命令")); err != nil {
				return err
			}
		}
	}
}

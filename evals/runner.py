"""Isolated V2 Workflow experiments using Phoenix's public client APIs."""
import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
import uuid

import httpx
from jsonschema import Draft202012Validator, FormatChecker
from opentelemetry.trace import Status, StatusCode
from phoenix.client import Client
from phoenix.otel import register

from evaluators import OllamaJudge, execution_success, reference_rules, source_integrity, skipped, JUDGE_VERSION

ROOT = Path(__file__).resolve().parents[1]
EVAL_DIR = Path(__file__).resolve().parent


def load_cases(path):
    raw = Path(path).read_bytes()
    cases = json.loads(raw)
    schema = json.loads((EVAL_DIR / "cases.schema.json").read_text())
    Draft202012Validator(schema, format_checker=FormatChecker()).validate(cases)
    ids = [c["id"] for c in cases]
    if len(ids) != len(set(ids)):
        raise ValueError("重复 case id")
    for c in cases:
        expected, input = c.get("expected", {}), c["input"]
        if input["kind"] == "extract" and "answerStatus" in expected:
            raise ValueError("extract 不能配置 answerStatus")
        if input["kind"] == "qa" and "suggestionCount" in expected:
            raise ValueError("qa 不能配置 suggestionCount")
    return cases, hashlib.sha256(raw).hexdigest()


def utcnow():
    return datetime.now(timezone.utc)


def dumps(value):
    return json.dumps(value, ensure_ascii=False)


def trace_id(span):
    return f"{span.get_span_context().trace_id:032x}"


def build_binary(path):
    subprocess.run(["go", "build", "-o", str(path), "./cmd/workflow-eval"], cwd=ROOT / "services/api", check=True)


def run_workflow(binary, input, args):
    # Each invocation creates temporary fixture/state inside Go. No HTTP debug mutation.
    with tempfile.TemporaryDirectory(prefix="jetaime-eval-input-") as workspace:
        environment = os.environ.copy()
        for name in ["TMPDIR", "TMP", "TEMP"]:
            environment[name] = workspace
        process = subprocess.run([str(binary), "-model", args.model, "-ollama-url", args.ollama_url,
                                  "-model-timeout", f"{args.timeout}s", "-context-tokens", str(args.context_tokens)],
                                 input=dumps(input), text=True, capture_output=True, timeout=args.timeout + 15,
                                 env=environment)
    if process.returncode:
        raise RuntimeError(process.stderr.strip() or "workflow-eval failed")
    output = json.loads(process.stdout)
    if not isinstance(output.get("run"), dict):
        raise ValueError("Workflow result missing run")
    return output


def emit_steps(tracer, output):
    # Replay measured Go timings as child spans; these are observations, not extra model calls.
    for step in output.get("run", {}).get("steps", []):
        start = datetime.fromisoformat(step["startedAt"].replace("Z", "+00:00"))
        start_ns = int(start.timestamp() * 1_000_000_000)
        attrs = {"openinference.span.kind": "LLM" if step["name"] == "model_call" else "CHAIN",
                 "workflow.observed_step": True, "workflow.step_status": step["status"],
                 "input.value": dumps(step.get("input")), "input.mime_type": "application/json",
                 "output.value": dumps(step.get("output")), "output.mime_type": "application/json"}
        if step["name"] == "model_call":
            request, result = step["input"], step.get("output", {})
            attrs["llm.model_name"] = request["model"]
            attrs["llm.invocation_parameters"] = dumps(request.get("options", {}))
            for i, message in enumerate(request["messages"]):
                attrs[f"llm.input_messages.{i}.message.role"] = message["role"]
                attrs[f"llm.input_messages.{i}.message.content"] = message["content"]
            attrs["llm.output_messages.0.message.role"] = "assistant"
            attrs["llm.output_messages.0.message.content"] = result.get("content", "")
            for key, name in [("promptTokens", "prompt"), ("completionTokens", "completion")]:
                if result.get(key) is not None:
                    attrs[f"llm.token_count.{name}"] = result[key]
        span = tracer.start_span(step["name"], start_time=start_ns, attributes=attrs)
        if step.get("error"):
            span.set_status(Status(StatusCode.ERROR, step["error"]))
        span.end(end_time=start_ns + step.get("durationNs", 0))


def preflight(args):
    with httpx.Client(timeout=10) as http:
        version = http.get(args.phoenix_url.rstrip("/") + "/arize_phoenix_version")
        version.raise_for_status()
        tags = http.get(args.ollama_url.rstrip("/") + "/api/tags")
        tags.raise_for_status()
        names = {m["name"] for m in tags.json().get("models", [])}
        for model in [args.model] + ([args.judge_model] if args.judge_model else []):
            if model not in names:
                raise ValueError(f"Ollama 缺少模型 {model}；已安装：{sorted(names)}")
    return {"version": version.text.strip(), "modelDigests": {m["name"]: m.get("digest") for m in tags.json().get("models", []) if m["name"] in [args.model, args.judge_model]}}


def run_experiment(args, cases, digest):
    version = preflight(args)
    client = Client(base_url=args.phoenix_url)
    suffix = uuid.uuid4().hex[:8]
    # Fresh immutable snapshot per launch avoids append/upsert changes to older experiment inputs.
    name = args.dataset + "-" + digest[:10] + "-" + suffix
    dataset = client.datasets.create_dataset(name=name, inputs=[c["input"] for c in cases],
        outputs=[c.get("expected", {}) for c in cases],
        metadata=[{**c.get("metadata", {}), "caseId": c["id"], "casesSha256": digest} for c in cases],
        dataset_description="V2 Workflow inputs and human references; snapshot per experiment", timeout=60)
    revision = subprocess.run(["git", "rev-parse", "HEAD"], cwd=ROOT, capture_output=True, text=True).stdout.strip()
    dirty = bool(subprocess.run(["git", "status", "--porcelain"], cwd=ROOT, capture_output=True, text=True).stdout)
    source_hash = hashlib.sha256()
    for path in sorted(list((ROOT / "services/api/internal/assistant").glob("*.go")) + list((ROOT / "services/api/cmd/workflow-eval").glob("*.go"))):
        source_hash.update(path.name.encode()); source_hash.update(path.read_bytes())
    experiment = client.experiments.create(dataset_id=dataset.id, dataset_version_id=dataset.version_id,
        experiment_name=args.name or "v2-" + utcnow().strftime("%Y%m%dT%H%M%SZ"), repetitions=args.repetitions,
        experiment_metadata={"casesSha256": digest, "workflowSourceSha256": source_hash.hexdigest(),
            "gitRevision": revision, "gitDirty": dirty, "model": args.model, "contextTokens": args.context_tokens,
            "judgeModel": args.judge_model, "judgeRubric": JUDGE_VERSION, "serviceVersions": version, "evaluatorSourceSha256": hashlib.sha256((EVAL_DIR / "evaluators.py").read_bytes()).hexdigest()})
    task_provider = register(endpoint=args.phoenix_url.rstrip("/") + "/v1/traces", protocol="http/protobuf",
        project_name="jetaime-v2-experiments", batch=True, set_global_tracer_provider=False, verbose=False)
    eval_provider = register(endpoint=args.phoenix_url.rstrip("/") + "/v1/traces", protocol="http/protobuf",
        project_name="jetaime-v2-evaluators", batch=True, set_global_tracer_provider=False, verbose=False)
    task_tracer, eval_tracer = task_provider.get_tracer("workflow-eval"), eval_provider.get_tracer("workflow-evaluator")
    judge = OllamaJudge(args.ollama_url, args.judge_model, eval_tracer, args.timeout) if args.judge_model else None
    args.output.mkdir(parents=True, exist_ok=True)
    logfile = args.output / ("experiment-" + suffix + ".jsonl")
    totals, errors = {}, 0
    try:
        with tempfile.TemporaryDirectory(prefix="jetaime-eval-build-") as directory:
            binary = Path(directory) / "workflow-eval"
            build_binary(binary)
            with logfile.open("x", encoding="utf-8") as log:
                for example in dataset.examples:
                    input, expected = example["input"], example["output"]
                    case_id = example["metadata"]["caseId"]
                    for repetition in range(1, args.repetitions + 1):
                        start, output, task_error = utcnow(), {}, None
                        with task_tracer.start_as_current_span("workflow." + input["kind"], attributes={
                            "openinference.span.kind": "CHAIN", "eval.case_id": case_id,
                            "eval.experiment_id": experiment["id"], "eval.repetition": repetition,
                            "input.value": dumps(input), "input.mime_type": "application/json"}) as span:
                            task_trace = trace_id(span)
                            try:
                                output = run_workflow(binary, input, args)
                                emit_steps(task_tracer, output)
                                span.set_attribute("output.value", dumps(output)); span.set_attribute("output.mime_type", "application/json")
                                run = output["run"]
                                span.set_attribute("workflow.prompt_version", run.get("promptVersion", ""))
                                if output.get("error"):
                                    span.set_status(Status(StatusCode.ERROR, output["error"]))
                            except Exception as exc:
                                task_error = str(exc); span.record_exception(exc); span.set_status(Status(StatusCode.ERROR, task_error))
                        end = utcnow()
                        recorded = client.experiments.log_run(experiment_id=experiment["id"], dataset_example_id=example["id"],
                            repetition_number=repetition, output=output, start_time=start, end_time=end,
                            trace_id=task_trace, error=task_error)
                        evaluations = {}
                        if task_error:
                            errors += 1
                        else:
                            funcs = {
                                "execution_success": ("CODE", lambda: execution_success(output)),
                                "source_integrity": ("CODE", lambda: source_integrity(input, output)),
                                "reference_rules": ("CODE", lambda: reference_rules(input, output, expected)),
                                "semantic_quality": ("LLM", lambda: judge.evaluate(input, output, expected) if judge else skipped("未启用 judge-model", "pending_judge")),
                            }
                            for metric, (kind, evaluate) in funcs.items():
                                eval_start, result, eval_error = utcnow(), {}, None
                                with eval_tracer.start_as_current_span(metric, attributes={
                                    "openinference.span.kind": "EVALUATOR", "eval.experiment_run_id": recorded["id"],
                                    "eval.task_trace_id": task_trace, "input.value": dumps({"input": input, "output": output, "expected": expected}),
                                    "input.mime_type": "application/json"}) as eval_span:
                                    eval_trace = trace_id(eval_span)
                                    try:
                                        result = evaluate()
                                        eval_span.set_attribute("output.value", dumps(result)); eval_span.set_attribute("output.mime_type", "application/json")
                                    except Exception as exc:
                                        eval_error = str(exc); eval_span.record_exception(exc); eval_span.set_status(Status(StatusCode.ERROR, eval_error)); errors += 1
                                client.experiments.log_evaluation(experiment_run_id=recorded["id"], name=metric, annotator_kind=kind,
                                    start_time=eval_start, end_time=utcnow(), trace_id=eval_trace, error=eval_error, **result)
                                evaluations[metric] = result if not eval_error else {"error": eval_error}
                                bucket = totals.setdefault(metric, {"scored": 0, "passed": 0, "pending": 0, "errors": 0})
                                if eval_error: bucket["errors"] += 1
                                elif "score" in result:
                                    bucket["scored"] += 1; bucket["passed"] += int(result["score"] == 1.0)
                                else: bucket["pending"] += 1
                        log.write(dumps({"caseId": case_id, "repetition": repetition, "experimentRunId": recorded["id"],
                            "taskTraceId": task_trace, "output": output, "taskError": task_error, "evaluations": evaluations}) + "\n")
                        log.flush()
                        print(f"{case_id} [{repetition}/{args.repetitions}] {'ERROR' if task_error else output['run'].get('status', 'failed')}", flush=True)
    finally:
        task_provider.force_flush(); eval_provider.force_flush()
        task_provider.shutdown(); eval_provider.shutdown()
    summary = {"experimentId": experiment["id"], "datasetId": dataset.id, "datasetVersionId": dataset.version_id,
        "url": client.experiments.get_experiment_url(dataset_id=dataset.id, experiment_id=experiment["id"]),
        "caseCount": len(cases), "repetitions": args.repetitions, "metrics": totals, "errors": errors,
        "results": str(logfile), "semanticScoresAreModelJudgments": True}
    summary_path = args.output / ("summary-" + suffix + ".json")
    summary_path.write_text(json.dumps(summary, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps(summary, ensure_ascii=False, indent=2))
    return 1 if errors else 0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--cases", type=Path, default=EVAL_DIR / "cases.json")
    parser.add_argument("--phoenix-url", default="http://127.0.0.1:6006")
    parser.add_argument("--ollama-url", default="http://127.0.0.1:11434")
    parser.add_argument("--model", default="qwen3.5:9b")
    parser.add_argument("--judge-model", help="Explicitly enable semantic LLM judge; preferably independent from task model")
    parser.add_argument("--timeout", type=int, default=180)
    parser.add_argument("--context-tokens", type=int, default=8192)
    parser.add_argument("--repetitions", type=int, default=1)
    parser.add_argument("--dataset", default="jetaime-v2-workflow")
    parser.add_argument("--name")
    parser.add_argument("--output", type=Path, default=EVAL_DIR / "output")
    parser.add_argument("--check-only", action="store_true", help="Offline schema validation only; no upload or model invocation")
    parser.add_argument("--check-services", action="store_true", help="Read-only service / model checks; no upload or generation")
    args = parser.parse_args()
    if args.repetitions < 1 or args.timeout < 1 or args.context_tokens < 4096:
        parser.error("repetitions/timeout 必须为正，context-tokens >=4096")
    cases, digest = load_cases(args.cases)
    if args.check_only or args.check_services:
        print(f"案例文件结构合法：{len(cases)} 条；sha256={digest}")
        if args.check_services:
            print("Phoenix/Ollama 可访问；" + dumps(preflight(args)))
        if not cases:
            print("案例待提供，尚未执行正式评测。")
        return 0
    if not cases:
        parser.error("案例为空，等待用户提供案例及预期结果；不会创建空实验")
    return run_experiment(args, cases, digest)


if __name__ == "__main__":
    raise SystemExit(main())

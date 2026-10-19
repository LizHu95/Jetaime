"""Local Workflow evaluation with immutable snapshots and JSONL reports."""
import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import uuid

import httpx
from jsonschema import Draft202012Validator, FormatChecker
from opentelemetry.trace import NoOpTracer

from evaluators import OllamaJudge, execution_success, reference_rules, source_integrity, skipped, JUDGE_VERSION, REFERENCE_RULES_VERSION

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
        if input["kind"] == "qa" and any(key in expected for key in ["requiredSuggestions", "forbiddenSuggestions"]):
            raise ValueError("qa 不能配置待办行动断言")
    return cases, hashlib.sha256(raw).hexdigest()


def utcnow():
    return datetime.now(timezone.utc)


def dumps(value):
    return json.dumps(value, ensure_ascii=False)


def build_binary(path):
    subprocess.run(["go", "build", "-o", str(path), "./cmd/workflow-eval"], cwd=ROOT / "services/api", check=True)


def model_arguments(args):
    flags = ["-model", args.model, "-ollama-url", args.ollama_url,
             "-model-timeout", f"{args.timeout}s", "-context-tokens", str(args.context_tokens),
             "-provider", getattr(args, "provider", "ollama"),
             "-api-key-env", getattr(args, "api_key_env", "DASHSCOPE_API_KEY"),
             "-output-mode", getattr(args, "output_mode", "json_schema")]
    for attr, flag in [("enable_thinking", "-enable-thinking"), ("model_url", "-model-url"), ("model_config", "-model-config"), ("model_profile", "-model-profile")]:
        value = getattr(args, attr, None)
        if value:
            flags += [flag, str(value)]
    return flags


def run_workflow(binary, input, args):
    # Each invocation creates temporary fixture/state inside Go. No HTTP debug mutation.
    with tempfile.TemporaryDirectory(prefix="jetaime-eval-input-") as workspace:
        environment = os.environ.copy()
        for name in ["TMPDIR", "TMP", "TEMP"]:
            environment[name] = workspace
        process = subprocess.run([str(binary), *model_arguments(args)],
                                 input=dumps(input), text=True, capture_output=True, timeout=args.timeout + 15,
                                 env=environment)
    if process.returncode:
        raise RuntimeError(process.stderr.strip() or "workflow-eval failed")
    output = json.loads(process.stdout)
    if not isinstance(output.get("run"), dict):
        raise ValueError("Workflow result missing run")
    return output


def preflight(args):
    # Resolve profiles through the same Go factory used by the debug console.
    process = subprocess.run(["go", "run", "./cmd/workflow-eval", *model_arguments(args), "-print-model-config"],
                             cwd=ROOT / "services/api", text=True, capture_output=True, timeout=180)
    if process.returncode:
        raise ValueError(process.stderr.strip() or "invalid model configuration")
    config = json.loads(process.stdout)
    selected_profile = config.pop("selected_profile", None)
    if selected_profile:
        args.model_profile = selected_profile
    args.model = config["model"]
    args.context_tokens = config["context_tokens"]
    args.model_url = config["base_url"]
    args.provider = config["provider"]
    args.resolved_model_config = config
    with httpx.Client(timeout=10) as http:
        digests = {}
        targets = {}
        if args.provider == "ollama":
            targets.setdefault(args.model_url, []).append(args.model)
        if args.judge_model:
            targets.setdefault(args.ollama_url, []).append(args.judge_model)
        for base, models in targets.items():
            tags = http.get(base.rstrip("/") + "/api/tags")
            tags.raise_for_status()
            names = {m["name"] for m in tags.json().get("models", [])}
            for model in models:
                if model not in names and model + ":latest" not in names:
                    raise ValueError(f"Ollama 缺少模型 {model}；已安装：{sorted(names)}")
            digests[base] = {m["name"]: m.get("digest") for m in tags.json().get("models", []) if m["name"] in models or m["name"].removesuffix(":latest") in models}
    return {"modelDigests": digests, "modelConfig": config,
            "cloudInferenceVerified": False}


def run_experiment(args, cases, digest):
    version = preflight(args)
    suffix = uuid.uuid4().hex[:8]
    args.output.mkdir(parents=True, exist_ok=True)
    snapshot = args.output / ("snapshot-" + suffix)
    snapshot.mkdir()
    # Freeze the selected public profile; later edits must not change an experiment.
    if getattr(args, "model_config", None):
        args.model_profile = args.model_profile or "evaluation"
        args.model_config = (snapshot / "model-config.json").resolve()
        args.model_config.write_text(dumps({"models": {args.model_profile: args.resolved_model_config}}))
    case_bytes = args.cases.read_bytes()
    if hashlib.sha256(case_bytes).hexdigest() != digest:
        raise ValueError("案例文件在载入后已改变，拒绝运行")
    (snapshot / "cases.json").write_bytes(case_bytes)
    # Fresh immutable snapshot per launch avoids append/upsert changes to older experiment inputs.
    name = args.dataset + "-" + digest[:10] + "-" + suffix
    revision = subprocess.run(["git", "rev-parse", "HEAD"], cwd=ROOT, capture_output=True, text=True).stdout.strip()
    dirty = bool(subprocess.run(["git", "status", "--porcelain"], cwd=ROOT, capture_output=True, text=True).stdout)
    source_hash = hashlib.sha256()
    for path in sorted(list((ROOT / "services/api/internal").rglob("*.go")) + list((ROOT / "services/api/cmd/workflow-eval").glob("*.go"))):
        source_hash.update(str(path.relative_to(ROOT)).encode()); source_hash.update(path.read_bytes())
        saved = snapshot / "source" / path.relative_to(ROOT)
        saved.parent.mkdir(parents=True, exist_ok=True)
        saved.write_bytes(path.read_bytes())
    (snapshot / "evaluators.py").write_bytes((EVAL_DIR / "evaluators.py").read_bytes())
    (snapshot / "runner.py").write_bytes(Path(__file__).read_bytes())
    (snapshot / "cases.schema.json").write_bytes((EVAL_DIR / "cases.schema.json").read_bytes())
    for path in [ROOT / "services/api/go.mod", ROOT / "services/api/go.sum", EVAL_DIR / "pyproject.toml", EVAL_DIR / "uv.lock"]:
        saved = snapshot / path.relative_to(ROOT)
        saved.parent.mkdir(parents=True, exist_ok=True)
        saved.write_bytes(path.read_bytes())
    run_metadata = {"suiteVersion": args.suite_version, "casesSha256": digest,
        "casesPath": str(args.cases.resolve()), "workflowSourceSha256": source_hash.hexdigest(),
        "gitRevision": revision, "gitDirty": dirty, "model": args.model,
        "modelConfig": args.resolved_model_config, "modelProfile": getattr(args, "model_profile", None),
        "contextTokens": args.context_tokens, "timeoutSeconds": args.timeout,
        "repetitions": args.repetitions, "judgeModel": args.judge_model,
        "judgeRubric": JUDGE_VERSION, "referenceRulesVersion": REFERENCE_RULES_VERSION, "serviceVersions": version,
        "evaluatorSourceSha256": hashlib.sha256((EVAL_DIR / "evaluators.py").read_bytes()).hexdigest()}
    experiment_id = uuid.uuid4().hex
    run_metadata["experimentName"] = args.name or "v2-" + utcnow().strftime("%Y%m%dT%H%M%SZ")
    (snapshot / "manifest.json").write_text(json.dumps(run_metadata, ensure_ascii=False, indent=2) + "\n")
    judge = OllamaJudge(args.ollama_url, args.judge_model, NoOpTracer(), args.timeout) if args.judge_model else None
    logfile = args.output / ("experiment-" + suffix + ".jsonl")
    totals, errors = {}, 0
    with tempfile.TemporaryDirectory(prefix="jetaime-eval-build-") as directory:
        binary = Path(directory) / "workflow-eval"
        build_binary(binary)
        with logfile.open("x", encoding="utf-8") as log:
            for case in cases:
                input, expected, case_id = case["input"], case.get("expected", {}), case["id"]
                for repetition in range(1, args.repetitions + 1):
                    start, output, task_error = utcnow(), {}, None
                    try:
                        output = run_workflow(binary, input, args)
                    except Exception as exc:
                        task_error = str(exc)
                    evaluations = {}
                    if task_error:
                        errors += 1
                    else:
                        funcs = {
                            "execution_success": lambda: execution_success(output),
                            "source_integrity": lambda: source_integrity(input, output),
                            "reference_rules": lambda: reference_rules(input, output, expected),
                            "semantic_quality": lambda: judge.evaluate(input, output, expected) if judge else skipped("未启用 judge-model", "pending_judge"),
                        }
                        for metric, evaluate in funcs.items():
                            try:
                                result = evaluate()
                            except Exception as exc:
                                result = {"error": str(exc)}
                                errors += 1
                            evaluations[metric] = result
                            bucket = totals.setdefault(metric, {"scored": 0, "passed": 0, "pending": 0, "errors": 0})
                            if "error" in result:
                                bucket["errors"] += 1
                            elif "score" in result:
                                bucket["scored"] += 1
                                bucket["passed"] += int(result["score"] == 1.0)
                            else:
                                bucket["pending"] += 1
                    log.write(dumps({"caseId": case_id, "repetition": repetition, "experimentRunId": uuid.uuid4().hex,
                        "startedAt": start.isoformat(), "finishedAt": utcnow().isoformat(),
                        "output": output, "taskError": task_error, "evaluations": evaluations}) + "\n")
                    log.flush()
                    print(f"{case_id} [{repetition}/{args.repetitions}] {'ERROR' if task_error else output['run'].get('status', 'failed')}", flush=True)
    summary = {"experimentId": experiment_id, "datasetId": name, "datasetVersionId": digest,
        "caseCount": len(cases), "repetitions": args.repetitions, "metrics": totals, "errors": errors,
        "results": str(logfile), "snapshot": str(snapshot), "metadata": run_metadata,
        "semanticScoresAreModelJudgments": True}
    summary_path = args.output / ("summary-" + suffix + ".json")
    summary_path.write_text(json.dumps(summary, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps(summary, ensure_ascii=False, indent=2))
    return 1 if errors else 0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--cases", type=Path, default=ROOT / "local/data/evals/cases.json")
    parser.add_argument("--suite-version", default="unversioned-draft", help="Frozen case bundle version; recorded in experiment metadata")
    parser.add_argument("--ollama-url", default="http://127.0.0.1:11434")
    parser.add_argument("--model", default="qwen3.5:9b")
    parser.add_argument("--provider", choices=["ollama", "openai-compatible"], default="ollama")
    parser.add_argument("--model-url")
    parser.add_argument("--enable-thinking", choices=["true", "false"])
    parser.add_argument("--api-key-env", default="DASHSCOPE_API_KEY")
    parser.add_argument("--output-mode", choices=["json_schema", "json_object"], default="json_schema")
    parser.add_argument("--model-config", type=lambda p: Path(p).resolve())
    parser.add_argument("--model-profile")
    parser.add_argument("--judge-model", help="Explicitly enable semantic LLM judge; preferably independent from task model")
    parser.add_argument("--timeout", type=int, default=180)
    parser.add_argument("--context-tokens", type=int, default=8192)
    parser.add_argument("--repetitions", type=int, default=1)
    parser.add_argument("--dataset", default="jetaime-v2-workflow")
    parser.add_argument("--name")
    parser.add_argument("--output", type=Path, default=ROOT / "outputs/evals")
    parser.add_argument("--check-only", action="store_true", help="Offline schema validation only; no upload or model invocation")
    parser.add_argument("--check-services", action="store_true", help="Read-only service / model checks; no upload or generation")
    args = parser.parse_args()
    direct_flags = {"--provider", "--model-url", "--ollama-url", "--model", "--api-key-env", "--output-mode", "--enable-thinking", "--model-config"}
    explicit_model = any(arg.split("=", 1)[0] in direct_flags for arg in sys.argv[1:])
    default_config = ROOT / "local/models.json"
    if not args.model_config and default_config.is_file() and (args.model_profile or not explicit_model):
        args.model_config = default_config

    if args.repetitions < 1 or args.timeout < 1 or args.context_tokens < 4096:
        parser.error("repetitions/timeout 必须为正，context-tokens >=4096")
    cases, digest = load_cases(args.cases)
    manifest_path = args.cases.parent / "manifest.json"
    if manifest_path.is_file():
        manifest = json.loads(manifest_path.read_text())
        if manifest.get("casesSha256") != digest:
            parser.error("冻结案例的内容已改变，哈希与manifest不符；请创建新版本")
        suite = manifest.get("suiteVersion")
        if args.suite_version == "unversioned-draft":
            args.suite_version = suite or args.suite_version
        elif suite and args.suite_version != suite:
            parser.error("suite-version与冻结案例manifest不一致")
    if args.check_only or args.check_services:
        print(f"案例文件结构合法：{len(cases)} 条；sha256={digest}")
        if args.check_services:
            print("所需本地模型可访问，云端只校验配置；" + dumps(preflight(args)))
        if not cases:
            print("案例待提供，尚未执行正式评测。")
        return 0
    if not cases:
        parser.error("案例为空，等待用户提供案例及预期结果；不会创建空实验")
    return run_experiment(args, cases, digest)


if __name__ == "__main__":
    raise SystemExit(main())

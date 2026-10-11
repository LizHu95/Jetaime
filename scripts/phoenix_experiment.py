# /// script
# requires-python = ">=3.10"
# dependencies = ["arize-phoenix-client==3.5.0", "httpx==0.28.1"]
# ///
"""Run saved select requests against the local Jetaime debug API.

Commands and prerequisites: docs/ollama-evaluation.md.

Keep `make trace` and `make debug` running. Cases with fixture_setup use
temporary fixture copies and isolated API processes, traced to jetaime-eval.
Scores check request success, outcome and recommended IDs; the optional
Ollama judge evaluates explanation quality.
"""

import argparse
import copy
import hashlib
import json
import re
import subprocess
import tempfile
import time
from contextlib import contextmanager
from datetime import datetime
from pathlib import Path

import httpx
from phoenix.client import Client
from ollama_evaluator import OllamaJudge

REPO = Path(__file__).resolve().parents[1]


def fixture_setup(metadata):
    metadata = normalize_fields(metadata or {}, "metadata")
    setup = metadata.get("fixture_setup")
    if isinstance(setup, str):
        setup = json.loads(setup)
    # CSV import can merge JSON metadata objects into the metadata root.
    if setup is None and "candidate_scope" in metadata:
        setup = metadata
    return setup


def case_fixture(setup):
    data = (REPO / setup["base_fixture"]).read_bytes()
    if hashlib.sha256(data).hexdigest() != setup["base_fixture_sha256"]:
        raise ValueError("fixture 已变化，与数据集来源不一致；请先更新数据集")
    fixture = json.loads(data)
    scope = setup["candidate_scope"]
    notes = {n["id"]: n for n in fixture["dataset"]["notes"]}
    selected = set(scope["note_ids"])
    collections = fixture["dataset"]["collections"]
    authorized = {c["noteId"] for c in collections if c["spaceId"] == scope["space_id"]}
    if not selected or not selected <= authorized:
        raise ValueError("案例候选不属于原fixture的指定空间")
    if any(notes[n]["type"] != scope["note_type"] for n in selected):
        raise ValueError("案例候选类型不匹配")
    fixture["dataset"]["collections"] = [c for c in collections if
        c["spaceId"] != scope["space_id"] or
        notes[c["noteId"]]["type"] != scope["note_type"] or c["noteId"] in selected]
    for note_id, facts in setup["facts_overrides"].items():
        if note_id not in selected:
            raise ValueError("facts覆盖必须属于案例候选")
        fixture["facts"][note_id] = copy.deepcopy(facts)
    return fixture


@contextmanager
def isolated_api(binary, fixture, config, args):
    """Use a fresh process/store per case without rewriting the source fixture."""
    with tempfile.TemporaryDirectory(prefix="jetaime-case-") as folder:
        path = Path(folder) / "fixture.json"
        path.write_text(json.dumps(fixture, ensure_ascii=False), encoding="utf-8")
        with (Path(folder) / "api.log").open("w+") as log:
            command = [str(binary), "-addr", "127.0.0.1:0", "-fixture", str(path),
                       "-ollama-url", config["ollamaUrl"],
                       "-model", args.model or config["model"],
                       "-model-timeout", f"{args.timeout:g}s",
                       "-context-tokens", str(config["contextTokens"]),
                       "-trace", "-trace-endpoint", args.phoenix_url.rstrip("/") + "/v1/traces",
                       "-trace-project", "jetaime-eval"]
            process = subprocess.Popen(command, stdout=log, stderr=log)
            try:
                deadline = time.monotonic() + 15
                while time.monotonic() < deadline:
                    log.seek(0)
                    contents = log.read()
                    match = re.search(r"本地调试台：http://(127\.0\.0\.1:\d+)", contents)
                    if process.poll() is not None:
                        raise RuntimeError("临时API启动失败：" + contents)
                    if match:
                        with httpx.Client(base_url="http://" + match[1], timeout=args.timeout,
                                          trust_env=False) as api:
                            response = api.get("/api/config")
                            response.raise_for_status()
                            yield api
                        return
                    time.sleep(0.05)
                raise TimeoutError("临时API启动超时")
            finally:
                process.terminate()
                try:
                    process.wait(timeout=8)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()


def normalize_fields(value, prefix):
    """Accept nested SDK objects and dotted CSV columns imported by Phoenix."""
    if not isinstance(value, dict):
        return value
    result = {}
    for key, item in value.items():
        path = key.removeprefix(prefix + ".").split(".")
        if isinstance(item, str):
            if path[-1] in ("budgetMaxCents", "durationMaxMinutes"):
                item = None if item.strip() in ("", "null") else int(item)
            elif path[-1] in ("noteTypes", "hardConstraints", "softPreferences", "options"):
                item = json.loads(item) if item.strip() else []
        target = result
        for part in path[:-1]:
            target = target.setdefault(part, {})
        target[path[-1]] = item
    return result


def request_body(value, model=None):
    value = normalize_fields(value, "input")
    if not isinstance(value, dict):
        raise ValueError("样例 input 必须是 JSON 对象")
    # Support both debug.decisions and decision.generate root span inputs.
    request = value.get("request", value)
    if not isinstance(request, dict) or request.get("task") != "select":
        raise ValueError("只支持包含 select request 的根 span 样例")
    if request.get("sessionId"):
        raise ValueError("续轮样例需要会话状态，当前脚本只支持新请求")
    for key in ("spaceId", "requesterId", "conditions"):
        if key not in request:
            raise ValueError(f"request 缺少 {key}")
    body = {"request": copy.deepcopy(request), "provider": "ollama"}
    selected_model = model or value.get("model")
    if selected_model:
        body["model"] = selected_model
    return body


def outcome(value):
    value = normalize_fields(value, "output")
    if not isinstance(value, dict):
        return None
    decision = value.get("decision", value)
    if not isinstance(decision, dict):
        return None
    result = decision.get("result", {})
    return result.get("outcome") if isinstance(result, dict) else None


def request_succeeded(output):
    return bool(outcome(output)) and not output.get("error")


def outcome_matches_reference(output, expected):
    reference = outcome(expected)
    if reference is None:
        raise ValueError("参考 output 缺少 result.outcome，请先修正样例")
    return request_succeeded(output) and outcome(output) == reference


def recommended_ids_match(output, expected):
    if not request_succeeded(output):
        return False
    def ids(value):
        value = normalize_fields(value, "output")
        return sorted(o["selection"]["noteId"] for o in
                      value.get("decision", value)["result"].get("options", []))
    return ids(output) == ids(expected)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dataset", default="jetaime-select-regression")
    parser.add_argument("--name", default="run-" + datetime.now().strftime("%Y%m%d-%H%M%S"))
    parser.add_argument("--phoenix-url", default="http://127.0.0.1:6006")
    parser.add_argument("--api-url", default="http://127.0.0.1:8080")
    parser.add_argument("--model", help="覆盖所有样例的模型；默认使用样例模型或服务默认模型")
    parser.add_argument("--timeout", type=float, default=240, help="单条请求超时秒数")
    parser.add_argument("--judge-model", help="启用 Ollama 质量评测，并指定评测模型")
    parser.add_argument("--ollama-url", default="http://127.0.0.1:11434", help="评测用 Ollama 地址")
    parser.add_argument("--judge-timeout", type=float, default=240, help="单条评测超时秒数")
    parser.add_argument("--judge-context-tokens", type=int, default=16384)
    parser.add_argument("--check-only", action="store_true", help="只检查连接和样例，不调用模型、不创建实验")
    args = parser.parse_args()
    judge = None
    if args.judge_model:
        judge = OllamaJudge(args.judge_model, args.ollama_url, args.judge_timeout,
                            args.judge_context_tokens)
        judge.check()
    client = Client(base_url=args.phoenix_url)
    dataset = client.datasets.get_dataset(dataset=args.dataset)
    if not dataset.examples:
        raise ValueError("数据集为空，请先添加样例")
    with tempfile.TemporaryDirectory(prefix="jetaime-experiment-") as folder, \
            httpx.Client(base_url=args.api_url, timeout=args.timeout, trust_env=False) as api:
        config_response = api.get("/api/config")
        config_response.raise_for_status()
        config = config_response.json()
        for example in dataset.examples:
            request_body(example["input"], args.model)
            if outcome(example["output"]) is None:
                raise ValueError(f"样例 {example['id']} 的参考 output 缺少 result.outcome")
        print(f"数据集：{args.dataset}，样例数：{len(dataset.examples)}", flush=True)
        print(f"API：{args.api_url}，fixture：{config.get('fixturePath')}", flush=True)
        setups = [fixture_setup(example.get("metadata")) for example in dataset.examples]
        requires_setup = any(setups)
        for setup in setups:
            if setup:
                case_fixture(setup)
        if args.check_only:
            print("连接、输入格式、参考结果和mock前置条件检查通过。")
            print("去掉 --check-only 并设置 --name baseline 即可运行。")
            return
        binary = Path(folder) / "api"
        if requires_setup:
            print("编译临时API；每条案例独立运行，Trace项目：jetaime-eval", flush=True)
            subprocess.run(["go", "build", "-o", str(binary), "./cmd/api"],
                           cwd=REPO / "services/api", check=True, timeout=120)

        def call_api(target, input):
            response = target.post("/api/decisions", json=request_body(input, args.model))
            value = response.json()
            if not response.is_success:
                return {"error": value.get("error", f"HTTP {response.status_code}")}
            if not isinstance(value.get("decision"), dict):
                raise ValueError("API 没有返回 decision")
            # Exclude large debug traces and transient session state.
            return {"decision": value["decision"]}

        def task(input, metadata):
            setup = fixture_setup(metadata)
            label = normalize_fields(metadata or {}, "metadata").get("case_id", "")
            print(f"运行案例 {label}", flush=True)
            if setup:
                with isolated_api(binary, case_fixture(setup), config, args) as target:
                    return call_api(target, input)
            return call_api(api, input)

        evaluators = {
            "request_succeeded": request_succeeded,
            "outcome_matches_reference": outcome_matches_reference,
            "recommended_ids_match": recommended_ids_match,
        }
        if judge:
            evaluators["ollama_recommendation_quality"] = judge.phoenix_evaluator()
        experiment = client.experiments.run_experiment(
            dataset=dataset,
            task=task,
            evaluators=evaluators,
            experiment_name=args.name,
            experiment_metadata={
                "api_url": args.api_url,
                "model_override": args.model,
                "server_model": config.get("model"),
                "fixture_path": config.get("fixturePath"),
                "reference_policy": "outcome and recommendation IDs; optional Ollama quality judge",
                "per_case_fixture": requires_setup,
                "trace_project": "jetaime-eval" if requires_setup else None,
                "judge_model": args.judge_model,
                "judge_rubric_version": "jetaime-quality-v1" if judge else None,
            },
            timeout=max(args.timeout, args.judge_timeout) if judge else args.timeout,
            retries=0,
        )
        print(client.experiments.get_experiment_url(dataset.id, experiment["experiment_id"]))


if __name__ == "__main__":
    main()

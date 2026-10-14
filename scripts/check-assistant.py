#!/usr/bin/env python3
"""Run the real local workflow against imported history, with disposable state."""
import argparse
import json
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


def request(base, path, body=None, timeout=360):
    data = None if body is None else json.dumps(body, ensure_ascii=False).encode()
    req = urllib.request.Request(base + path, data=data, headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as response:
            return json.load(response)
    except urllib.error.HTTPError as error:
        detail = json.load(error)
        raise RuntimeError(detail.get("error", str(detail))) from error


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--model", default="qwen3.5:9b")
    parser.add_argument("--ollama-url", default="http://127.0.0.1:11434")
    parser.add_argument("--reuse-model-results", action="store_true", help="Reuse verified real-model results from the output report")
    parser.add_argument("--output", type=Path, default=ROOT / "outputs/agent-check/acceptance.json")
    args = parser.parse_args()
    fixture = ROOT / "local/data/chat-history/history.fixture.json"
    history = json.loads(fixture.read_text())
    group, actor = "wechat-zzj1031", "wechat-l"
    if not any(m["id"] == "wx-0165" for m in history["messages"]):
        raise RuntimeError("This check expects the imported WeChat history and its wx-0165 source.")
    previous = json.loads(args.output.read_text()) if args.reuse_model_results else {}
    report = {"model": args.model, "historyMessages": len(history["messages"]), "checks": []}
    process = None
    with tempfile.TemporaryDirectory(prefix="jetaime-agent-check-") as directory:
        directory = Path(directory)
        binary = directory / "assistant"
        subprocess.run(["go", "build", "-o", str(binary), "./cmd/api"], cwd=ROOT / "services/api", check=True)
        command = [str(binary), "-assistant", "-addr", "127.0.0.1:0", "-fixture", str(fixture), "-state", str(directory / "state.json"), "-log", str(directory / "runs.jsonl"), "-context-tokens", "98304", "-model-timeout", "5m", "-model", args.model, "-ollama-url", args.ollama_url]

        def start():
            server = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
            for line in server.stdout:
                if line.startswith("HTTP API: "):
                    return server, line.strip().removeprefix("HTTP API: ")
            server.wait(timeout=10)
            raise RuntimeError("Could not start isolated HTTP API")

        def stop(server):
            server.terminate()
            try:
                server.wait(timeout=10)
            except subprocess.TimeoutExpired:
                server.kill()
                server.wait()

        try:
            process, base = start()
            scope = {"groupId": group, "actorId": actor}
            state_path = "/api/assistant/state?groupId=" + group + "&actorId=" + actor
            state = request(base, state_path)["context"]
            assert len(state["messages"]) == len(history["messages"]) and len(state["users"]) == 2
            report["checks"].append("imported_history_and_account_merge")
            print("Loaded imported history: 2 people, %d messages" % len(state["messages"]), flush=True)
            for kind, question in [("qa", "群里约定健身和理发分别是几点？请给出明确安排的原文来源。"), ("extract", "")]:
                started = time.time()
                run = previous.get(kind)
                if run is not None:
                    original = next(step["output"] for step in run["steps"] if step["name"] == "build_context")
                    assert run["model"] == args.model and run.get("question", "") == question
                    assert original["messages"] == history["messages"] and not original["tasks"]
                    print("Reusing verified real-model %s result" % kind, flush=True)
                else:
                    run = request(base, "/api/assistant/run", {**scope, "kind": kind, "question": question})["run"]
                report[kind] = run
                args.output.parent.mkdir(parents=True, exist_ok=True)
                args.output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
                assert run["status"] == "succeeded", run.get("error")
                assert all(step["status"] == "succeeded" for step in run["steps"])
                report["checks"].append(kind + "_real_model_and_source_validation")
                print("%s completed in %.1fs" % (kind, time.time() - started), flush=True)
            answer = report["qa"]["answer"]
            assert answer["status"] == "answered" and "wx-0165" in answer["sourceMessageIds"], answer
            report["checks"].append("qa_expected_source")
            suggestions = report["extract"].get("suggestions", [])
            # A dated history can return no new commitments. Replay an actual
            # positive window to test the write lifecycle without inventing chat.
            report["fullHistorySuggestions"] = len(suggestions)
            positive = dict(history)
            anchor = next(i for i, m in enumerate(history["messages"]) if m["id"] == "wx-0165")
            positive["messages"] = history["messages"][anchor - 4:anchor + 1]
            report["replaySourceIds"] = [m["id"] for m in positive["messages"]]
            replay_path = directory / "replay.json"
            replay_path.write_text(json.dumps(positive, ensure_ascii=False))
            stop(process)
            process = None
            command[command.index("-fixture") + 1] = str(replay_path)
            process, base = start()
            replay = request(base, "/api/assistant/run", {**scope, "kind": "extract", "question": ""})["run"]
            report["historicalReplayExtraction"] = replay
            assert replay["status"] == "succeeded"
            suggestions = replay.get("suggestions", [])
            assert suggestions and any("wx-0165" in s["sourceMessageIds"] for s in suggestions), "Known explicit schedule was missed in its original historical window."
            report["checks"].append("historical_replay_positive_extraction")
            state = request(base, state_path)["context"]
            print("Historical replay extracted %d suggestions from actual source messages" % len(suggestions), flush=True)
            suggestion = next((s for s in suggestions if s.get("assigneeId")), None)
            assert suggestion, "No assigned action to verify confirmation."
            source = {m["id"]: m for m in state["messages"]}
            for s in suggestions:
                assert s["sourceMessageIds"] and all(i in source for i in s["sourceMessageIds"])
            body = {**scope, "key": suggestion["id"], "title": suggestion["title"], "assigneeId": suggestion["assigneeId"], "dueAt": "", "allowDuplicate": False}
            assert not request(base, state_path)["context"]["tasks"], "Extraction wrote tasks before confirmation."
            task = request(base, "/api/assistant/confirm", body)
            assert request(base, "/api/assistant/confirm", body)["id"] == task["id"]
            report["checks"].extend(["no_task_before_confirmation", "confirmation_idempotence"])
            completed = request(base, "/api/assistant/complete", {"groupId": group, "actorId": task["assigneeId"], "taskId": task["id"], "done": True})
            assert completed["status"] == "completed"
            report["checks"].append("assignee_completion")
            report["confirmedTask"] = completed
            stop(process)
            process = None
            process, base = start()
            restored = request(base, state_path)["context"]["tasks"]
            assert len(restored) == 1 and restored[0]["id"] == task["id"] and restored[0]["status"] == "completed"
            report["checks"].append("restart_persistence")
            traces = [json.loads(line) for line in (directory / "runs.jsonl").read_text().splitlines()]
            assert len(traces) == (1 if args.reuse_model_results else 3) and all(r["status"] == "succeeded" for r in traces)
            assert all(len(r["steps"]) >= 3 for r in traces)
            report["checks"].append("jsonl_trace_persistence")
            report["status"] = "passed"
            print("Confirmation, completion, restart and trace persistence passed.", flush=True)
        except Exception as error:
            report["status"] = "failed"
            report["error"] = str(error)
            raise
        finally:
            if process is not None:
                stop(process)
            args.output.parent.mkdir(parents=True, exist_ok=True)
            args.output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
            print("Report:", args.output, flush=True)


if __name__ == "__main__":
    main()

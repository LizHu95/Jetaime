#!/usr/bin/env python3
"""One local startup entry point; loads credentials without evaluating shell code."""
import os
from pathlib import Path
import re
import shlex
import sys

ROOT = Path(__file__).resolve().parents[1]


def load_env(path, environment, inherited):
    if not path.exists():
        return
    for number, line in enumerate(path.read_text().splitlines(), 1):
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        if line.startswith("export "):
            line = line[7:].strip()
        name, separator, value = line.partition("=")
        name = name.strip()
        if not separator or not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*", name):
            raise ValueError(f"{path.name}:{number}: invalid environment assignment")
        try:
            values = shlex.split(value, comments=True, posix=True)
        except ValueError:
            raise ValueError(f"{path.name}:{number}: invalid quoted value") from None
        if len(values) > 1:
            raise ValueError(f"{path.name}:{number}: quote values containing spaces")
        if name not in inherited:
            environment[name] = values[0] if values else ""


def command(action, args, environment):
    go = ROOT / "services/api"
    uv = environment.get("JETAIME_UV", "uv")
    if action in ["api", "assistant", "feishu", "model-check"]:
        program = "api" if action in ["api", "assistant"] else action
        flags = []
        if action == "assistant":
            flags = ["-assistant"]
        elif action == "model-check":
            program = "workflow-eval"
            flags = ["-print-model-config"]
        return go, ["go", "run", "./cmd/" + program, *flags, *args]
    if action in ["eval", "eval-check", "eval-services"]:
        flags = {"eval": [], "eval-check": ["--check-only"], "eval-services": ["--check-services"]}[action]
        return ROOT, [uv, "run", "--project", "evals", "--locked", "python", "evals/runner.py", *flags, *args]
    if action == "eval-smoke":
        return ROOT, [uv, "run", "--project", "evals", "--locked", "python", "evals/tests/smoke_phoenix.py", *args]
    if action == "assistant-check":
        return ROOT, ["python3", "scripts/check-assistant.py", *args]
    if action == "trace":
        return ROOT, ["bash", "deploy/phoenix/start.sh", *args]
    if action == "feishu-build":
        return ROOT, ["bash", "deploy/feishu/build.sh", *args]
    raise ValueError("unknown service; choose api, assistant, feishu, trace, eval, eval-check, eval-services, eval-smoke, assistant-check, model-check or feishu-build")


def main():
    action = sys.argv[1] if len(sys.argv) > 1 else "api"
    args = sys.argv[2:]
    if action in ["-h", "--help", "help"]:
        print("python3 scripts/dev.py [api|assistant|feishu|trace|eval|eval-check|eval-services|eval-smoke|assistant-check|model-check|feishu-build] [arguments]")
        return
    environment = os.environ.copy()
    inherited = set(environment)
    load_env(ROOT / "local/.env", environment, inherited)
    if action in ["feishu", "feishu-build"]:
        load_env(ROOT / "local/feishu/.env", environment, inherited)
    cwd, argv = command(action, args, environment)
    os.chdir(cwd)
    os.execvpe(argv[0], argv, environment)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError) as error:
        print(f"Startup failed: {error}", file=sys.stderr)
        sys.exit(1)

"""Resolve relocated artifacts without modifying historical experiment records."""
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


def relocated(path, root=ROOT):
    path = Path(path)
    if path.exists():
        return path
    absolute = path if path.is_absolute() else root / path
    mappings = [
        ("evals/output", "outputs/evals"),
        ("local/data/evals/output", "outputs/evals"),
        ("evals/versions", "local/data/evals/versions"),
        ("local/data/chat-eval", "outputs/evals/reports"),
        ("outputs/chat-eval", "outputs/evals/reports"),
        ("local/data/agent-check", "outputs/agent-check"),
        ("outputs/chat-history", "local/data/chat-history"),
    ]
    for old, new in mappings:
        try:
            return root / new / absolute.relative_to(root / old)
        except ValueError:
            continue
    return absolute

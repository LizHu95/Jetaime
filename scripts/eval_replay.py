#!/usr/bin/env python3
"""Build/run the isolated replay binary, or prepare draft cases. No DB access."""
import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile

from dev import load_env

ROOT = Path(__file__).resolve().parents[1]


def private_json(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    with os.fdopen(os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), 'w') as out:
        json.dump(value, out, ensure_ascii=False, indent=2)
        out.write('\n')


def source_info(root):
    def git(*args):
        return subprocess.check_output(['git', '-C', str(root), *args], text=True).strip()
    return {'branch': git('branch', '--show-current') or 'detached',
            'commit': git('rev-parse', 'HEAD'),
            'dirty': 'true' if git('status', '--porcelain') else 'false'}


def registration_source(root):
    """Mirror only the known production registration seam; never guess policy."""
    module = 'github.com/LizHu95/Jetaime/services/api/internal/ai/tools'
    production = (root / 'services/api/internal/app/modules.go').read_text()
    policy = 'eager'
    body = ''
    imports = f'import "{module}"\n'
    if 'search.New(catalog)' in production:
        policy = 'amap'
        imports = f'import ("{module}"; "{module}/search")\n'
        body = 'definitions := make([]tools.Definition, 0, len(entries)); for _, entry := range entries { definitions = append(definitions, entry.Definition()) }; entries = append(entries, search.New(definitions));'
    elif 'search.New(entries)' in production:
        policy = 'minimal'
        imports = f'import ("{module}"; "{module}/search")\n'
        body = 'entries = append(entries, search.New(entries));'
    elif 'search.New(' in production:
        raise ValueError('Unsupported production discovery registration; update replay bridge explicitly.')
    elif 'catalog.New(entries...)' in production:
        path = root / 'services/api/internal/ai/tools/catalog/catalog.go'
        return 'shared-catalog', path.read_text()
    return policy, 'package catalog\n' + imports + 'func New(entries ...tools.Tool) (*tools.Registry,error) {' + body + 'return tools.New(entries...)}\n'


def build(root):
    source = source_info(root)
    target = root / 'outputs/evals/replay/bin/eval-replay'
    target.parent.mkdir(parents=True, exist_ok=True)
    # One harness is overlaid onto the chosen branch without changing its files.
    # Agent, prompt and real tool implementations still come from that branch.
    relative = ['services/api/cmd/eval-replay/main.go']
    relative += [str(p.relative_to(ROOT)) for p in sorted((ROOT / 'services/api/internal/ai/replay').glob('*.go'))
                 if not p.name.endswith('_test.go')]
    harness = hashlib.sha256()
    for path in relative:
        harness.update(path.encode() + b'\0' + (ROOT / path).read_bytes())
    policy, catalog_source = registration_source(root)
    source.update({'harnessSHA256': harness.hexdigest(), 'toolPolicy': policy})
    # Every flag is passed as data to Go, never through a shell command.
    names = {'branch': 'buildBranch', 'commit': 'buildCommit', 'dirty': 'buildDirty',
             'harnessSHA256': 'buildHarnessSHA256', 'toolPolicy': 'buildToolPolicy'}
    flags = ' '.join('-X main.%s=%s' % (names[key], value) for key, value in source.items())
    with tempfile.TemporaryDirectory(prefix='jetaime-replay-build-') as temp:
        temp = Path(temp)
        adapter = temp / 'catalog.go'
        adapter.write_text(catalog_source)
        replace = {str(root / path): str(ROOT / path) for path in relative}
        replace[str(root / 'services/api/internal/ai/tools/catalog/catalog.go')] = str(adapter)
        overlay = temp / 'overlay.json'
        overlay.write_text(json.dumps({'Replace': replace}))
        subprocess.run(['go', 'build', '-overlay', str(overlay), '-ldflags', flags,
                        '-o', str(target), './cmd/eval-replay'],
                       cwd=root / 'services/api', check=True)
    return target


def model_environment(root, env_file=None):
    env = os.environ.copy()
    load_env(env_file or root / 'local/.env', env, set(env))
    # Neither the replay process nor SDK task receives business DB/map credentials.
    for key in ('JETAIME_DATABASE_URL', 'JETAIME_TEST_DATABASE_URL', 'JETAIME_GAODE_API_KEY'):
        env.pop(key, None)
    return env


def prepare(snapshot, output, limit):
    raw = snapshot.read_bytes()
    messages = [json.loads(line) for line in raw.splitlines()]
    messages.sort(key=lambda m: (m['groupId'], m['seq']))
    candidates = []
    for message in messages:
        if message['senderType'] != 'user' or not message.get('mentionsAI'):
            continue
        past = [m for m in messages if m['groupId'] == message['groupId'] and m['seq'] < message['seq']]
        if not past or not any(m['senderType'] == 'user' and m['senderId'] == message['senderId'] for m in past):
            continue
        candidates.append((message, past))
    # Most recent requests, then sort back into stable chronological order.
    candidates.sort(key=lambda pair: (pair[0]['createdAt'], pair[0]['id']))
    cases = []
    for message, past in candidates[-limit:]:
        references = [m['content'] for m in messages if m['groupId'] == message['groupId']
                      and m['senderType'] == 'assistant' and m.get('replyToId') == message['id']]
        cases.append({'id': 'historical-' + message['id'], 'groupId': message['groupId'],
                      'actorId': message['senderId'], 'cutoffSeq': max(m['seq'] for m in past),
                      'at': message['createdAt'], 'timezone': 'Asia/Shanghai',
                      'question': message['content'], 'replyToId': message.get('replyToId'),
                      'retrievedMessageIds': [], 'mapFixtures': [], 'reviewed': False,
                      'expected': {'reference': '\n'.join(references)}})
    if not cases:
        raise ValueError('没有可回放的历史 @AI 请求；请使用示例格式手工编写案例。')
    private_json(output, {'version': 'chat-replay-draft-v1',
                         'snapshotSha256': hashlib.sha256(raw).hexdigest(), 'cases': cases})
    print(json.dumps({'cases': len(cases), 'path': str(output), 'reviewed': False}, ensure_ascii=False))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--source-root', type=Path, default=ROOT)
    parser.add_argument('--env-file', type=Path, help='model credential environment file')
    commands = parser.add_subparsers(dest='command', required=True)
    prepare_args = commands.add_parser('prepare', help='prepare unreviewed cases from exported @AI messages')
    prepare_args.add_argument('--snapshot', type=Path, required=True)
    prepare_args.add_argument('--output', type=Path, required=True)
    prepare_args.add_argument('--limit', type=int, default=20)
    commands.add_parser('build')
    run = commands.add_parser('run')
    run.add_argument('arguments', nargs=argparse.REMAINDER)
    args = parser.parse_args()
    root = args.source_root.resolve()
    if args.command == 'prepare':
        if args.limit < 1:
            parser.error('--limit must be positive')
        prepare(args.snapshot.resolve(), args.output.resolve(), args.limit)
        return
    binary = build(root)
    if args.command == 'build':
        print(binary)
        return
    argv = args.arguments
    if argv and argv[0] == '--':
        argv = argv[1:]
    # User paths are relative to the shell's directory, not the Go module cwd.
    paths = {'-snapshot', '-cases', '-output', '-model-config'}
    normalized = list(argv)
    for index, value in enumerate(argv):
        if index and argv[index - 1] in paths and value != '-':
            normalized[index] = str(Path(value).resolve())
        elif '=' in value and value.split('=', 1)[0] in paths:
            name, path = value.split('=', 1)
            normalized[index] = name + '=' + (path if path == '-' else str(Path(path).resolve()))
    env_file = args.env_file.resolve() if args.env_file else None
    status = subprocess.run([str(binary), *normalized], cwd=root / 'services/api',
                            env=model_environment(root, env_file)).returncode
    sys.exit(status)


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        print('Replay failed: ' + str(error), file=sys.stderr)
        sys.exit(1)

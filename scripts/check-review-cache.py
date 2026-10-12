#!/usr/bin/env python3
"""Real Ollama cache checks: isolated server per case, repeated calls in one process."""
import argparse
import hashlib
import json
import subprocess
import tempfile
import time
from pathlib import Path
import urllib.request

ROOT = Path(__file__).resolve().parents[1]


def request(base, path, body=None):
    data = None if body is None else json.dumps(body, ensure_ascii=False).encode()
    req = urllib.request.Request(base + path, data=data, headers={'Content-Type': 'application/json'})
    with urllib.request.urlopen(req, timeout=360) as response:
        return json.load(response)


def normalized(run):
    return [{k: s.get(k) for k in ['title', 'assigneeId', 'timeExpression', 'sourceMessageIds']} for s in run.get('suggestions', [])]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--model', default='qwen3.5:9b')
    parser.add_argument('--ollama-url', default='http://127.0.0.1:11434')
    parser.add_argument('--cases', type=Path, default=ROOT/'local/data/evals/versions/chat-v1.1-extract6-reviewed-r1/cases.json')
    parser.add_argument('--output', type=Path, default=ROOT/'local/data/chat-eval/review-cache-check.json')
    args = parser.parse_args()
    raw = args.cases.read_bytes()
    cases = json.loads(raw)
    source_hash = hashlib.sha256()
    for path in sorted(list((ROOT/'services/api/internal/assistant').glob('*.go')) + list((ROOT/'services/api/cmd/assistant').glob('*.go'))):
        source_hash.update(str(path.relative_to(ROOT)).encode()); source_hash.update(path.read_bytes())
    report = {'workflowSourceSha256': source_hash.hexdigest(), 'promptVersion': 'extract-v7-cache', 'cachePolicy': {'maxEntries':64,'ttlSeconds':600,'uncertainNotCached':True}, 'status': 'running', 'model': args.model, 'casesSha256': hashlib.sha256(raw).hexdigest(),
              'casesPath': str(args.cases), 'mode': 'isolated real-model cache verification; not a Phoenix quality experiment',
              'cases': []}
    # Verify the review prompt/schema is exactly the original stable v4's.
    baseline = [json.loads(l) for l in (ROOT/'local/data/evals/output/experiment-8e938583.jsonl').read_text().splitlines()]
    old_review = next(s['input'] for r in baseline for s in r['output']['run']['steps'] if s['name']=='review_model_call')
    with tempfile.TemporaryDirectory(prefix='jetaime-review-cache-') as work:
        work = Path(work)
        binary = work/'assistant'
        subprocess.run(['go', 'build', '-o', str(binary), './cmd/assistant'], cwd=ROOT/'services/api', check=True)
        for case in cases:
            if case['input']['kind'] != 'extract':
                continue
            folder = work/case['id']; folder.mkdir()
            fixture = folder/'fixture.json'; fixture.write_text(json.dumps(case['input']['fixture'], ensure_ascii=False))
            process = subprocess.Popen([str(binary), '-experimental-review-cache', '-addr', '127.0.0.1:0', '-fixture', str(fixture),
                '-state', str(folder/'state.json'), '-log', str(folder/'runs.jsonl'), '-context-tokens', '16384',
                '-model-timeout', '5m', '-model', args.model, '-ollama-url', args.ollama_url],
                stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
            entry = {'caseId': case['id'], 'runs': []}
            stored_outputs = {}
            try:
                first = process.stdout.readline().strip()
                if not first.startswith('V2 demo: '):
                    raise RuntimeError(first)
                base = first[len('V2 demo: '):]
                scope = {'groupId': case['input']['groupId'], 'actorId': case['input']['actorId']}
                for repetition in range(1, 4):
                    start = time.monotonic()
                    run = request(base, '/api/run', {**scope, 'kind': 'extract'})['run']
                    assert run['status']=='succeeded'
                    calls = [s for s in run['steps'] if s['name'].endswith('model_call')]
                    for s in calls:
                        if s['name']=='review_model_call':
                            assert s['input']['messages'][0] == old_review['messages'][0], 'Review logic changed'
                            assert s['input']['format'] == old_review['format']
                    cache = next((s['output'] for s in run['steps'] if s['name']=='review_cache'), {'status':'no_candidates'})
                    entry['runs'].append({'repetition': repetition, 'elapsedSeconds': time.monotonic()-start, 'cache':cache, 'run':run})
                    reviews = next((s['output']['reviews'] for s in run['steps'] if s['name']=='review_actions'), [])
                    if cache['status']=='hit':
                        assert normalized(run)==stored_outputs[cache['key']], 'Cached verdict changed business output'
                        assert not any(s['name']=='review_model_call' for s in run['steps'])
                    elif cache.get('stored'):
                        stored_outputs[cache['key']] = normalized(run)
                    if any(r['status']=='uncertain' for r in reviews):
                        assert cache['status']=='miss' and cache.get('stored') is False
                    print(case['id'], repetition, cache['status'], 'calls='+str(len(calls)), flush=True)
                if entry['runs'][0]['cache']['status']=='no_candidates':
                    assert all(r['cache']['status']=='no_candidates' for r in entry['runs'])
                if case['id']=='extract-explicit-schedule':
                    assert [r['cache']['status'] for r in entry['runs']]==['miss','hit','hit']
                    suggestions=entry['runs'][0]['run']['suggestions']
                    assert len(suggestions)==2 and all(s['assigneeId']=='wechat-l' for s in suggestions)
                if case['id']=='extract-completed-purchase':
                    suggestions=entry['runs'][0]['run'].get('suggestions', [])
                    assert not any('面包' in s['title'] for s in suggestions)
                    assert any('英语' in s['title'] for s in suggestions)
                    entry['futureReturnHomeKept'] = [any('回家' in s['title'] for s in r['run'].get('suggestions', [])) for r in entry['runs']]
                state = request(base, '/api/state?groupId='+scope['groupId']+'&actorId='+scope['actorId'])['context']
                assert not state['tasks'] and state['messages']==case['input']['fixture']['messages']
                report['cases'].append(entry)
                args.output.parent.mkdir(parents=True, exist_ok=True)
                args.output.write_text(json.dumps(report, ensure_ascii=False, indent=2)+'\n')
            finally:
                process.terminate()
                try: process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    process.kill();process.wait()
    report['status']='passed'
    args.output.write_text(json.dumps(report, ensure_ascii=False, indent=2)+'\n')
    print('Cache verification passed:', args.output, flush=True)


if __name__=='__main__':
    main()

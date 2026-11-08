#!/usr/bin/env python3
"""Prepare, run and summarize the fixed A/B/C tool discovery comparison."""
import argparse
from collections import Counter
from datetime import datetime, timezone
import hashlib
import json
import math
import os
from pathlib import Path
import statistics
import subprocess
import sys
import uuid

from eval_replay import ROOT, build, model_environment, private_json
from replay_judge import DIMENSIONS, ReplayJudge, evaluation_settings

SUITE = ROOT / 'evals/tool-search'
VARIANTS = {'A': 'feat/debug', 'B': 'codex/tool-search-b', 'C': 'codex/tool-search-c'}
SCORES = ('execution_success', 'map_fixtures_complete', 'required_tools',
          'forbidden_tools', 'answer_terms', 'required_sources',
          'reminder_operations', 'reminder_arguments', 'reminder_result')
METRICS = ('promptTokens', 'completionTokens', 'durationMs', 'searchCalls',
           'firstInputTokens', 'cumulativeInputTokens', 'totalTokens', 'modelCallCount')


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def git(root, *args):
    return subprocess.check_output(['git', '-C', str(root), *args], text=True).strip()


def private_copy(source, destination):
    destination.parent.mkdir(parents=True, exist_ok=True)
    with destination.open('xb') as out:
        destination.chmod(0o600)
        out.write(source.read_bytes())


def generated_text(path, content):
    temporary = path.with_name(path.name + '.' + uuid.uuid4().hex + '.tmp')
    with temporary.open('x') as out:
        temporary.chmod(0o600)
        out.write(content)
    os.replace(temporary, path)


def source_roots(overrides, baseline, commits=None, variants=None):
    """Resolve existing checkouts only; never switch, reset or create branches."""
    records = []
    for block in git(ROOT, 'worktree', 'list', '--porcelain').split('\n\n'):
        fields = dict(line.split(' ', 1) for line in block.splitlines() if ' ' in line)
        if 'worktree' in fields:
            records.append(fields)
    commits = {} if commits is None else commits
    if (not isinstance(commits, dict) or set(commits) - {'B', 'C'}
            or any(not isinstance(value, str) or len(value) != 40
                   or any(c not in '0123456789abcdef' for c in value)
                   for value in commits.values())):
        raise ValueError('variantCommits must contain full frozen B/C commit hashes')
    selected = list(VARIANTS) if variants is None else list(variants)
    if 'A' not in selected or len(set(selected)) != len(selected) or set(selected) - set(VARIANTS):
        raise ValueError('variants must be unique, known variants including baseline A')
    expected = {'A': git(ROOT, 'rev-parse', baseline)}
    expected.update({v: git(ROOT, 'rev-parse', commits.get(v, ref))
                     for v, ref in VARIANTS.items() if v != 'A' and v in selected})
    roots = {}
    for variant, commit in expected.items():
        candidates = [Path(r['worktree']) for r in records if r.get('HEAD') == commit]
        root = overrides.get(variant) or (candidates[0] if candidates else None)
        if root is None:
            raise ValueError(f'{variant}: no checkout for {commit}; supply --{variant.lower()}-root')
        root = root.resolve()
        if git(root, 'rev-parse', 'HEAD') != commit:
            raise ValueError(f'{variant}: checkout must match {commit}')
        subprocess.run(['git', '-C', str(root), 'merge-base', '--is-ancestor',
                        expected['A'], 'HEAD'], check=True)
        # Untracked UI work does not affect this comparison. Tracked Go edits do.
        changed = git(root, 'diff', 'HEAD', '--name-only', '--', 'services/api')
        changed = [name for name in changed.splitlines() if not name.endswith('_test.go')]
        untracked = git(root, 'ls-files', '--others', '--exclude-standard', '--', 'services/api')
        changed += [name for name in untracked.splitlines()
                    if name.endswith('.go') and not name.endswith('_test.go')]
        if changed:
            raise ValueError(f'{variant}: commit tracked API changes before comparing: {changed}')
        roots[variant] = root
    return roots


def load_inputs(cases_path, snapshot, protocol_path, phase, online):
    suite = json.loads(cases_path.read_text())
    protocol = json.loads(protocol_path.read_text())
    if suite.get('snapshotSha256') != digest(snapshot):
        raise ValueError('snapshotSha256 does not match the snapshot')
    cases = suite.get('cases', [])
    ids = [c['id'] for c in cases]
    expected_ids = [c['id'] for c in protocol['cases']]
    if len(ids) != len(set(ids)) or set(ids) != set(expected_ids):
        raise ValueError('suite must contain each protocol case exactly once')
    if online and (any(c.get('reviewed') is not True for c in cases)
                   or 'draft' in suite['version'].lower()):
        raise ValueError('Online comparison requires a new suite version with every case reviewed=true; '
                         'review the draft first. No model or Phoenix request was sent.')
    if phase == 'smoke':
        selected = set(protocol['smokeCaseIds'])
        if not selected or not selected <= set(ids):
            raise ValueError('invalid smokeCaseIds')
        suite = {**suite, 'version': suite['version'] + '-smoke',
                 'cases': [c for c in cases if c['id'] in selected]}
    blocked = {c['id'] for c in protocol['cases'] if c.get('readiness', '').startswith('blocked')}
    if online and blocked & {c['id'] for c in suite['cases']}:
        raise ValueError('Selected real cases lack replay state or map recordings; '
                         'restore evidence or freeze a matching suite/protocol subset first.')
    return suite, protocol


def preflight(roots, snapshot, cases, outdir):
    variants = {}
    for variant, root in roots.items():
        binary = build(root)
        frozen = outdir / 'bin' / variant / 'eval-replay'
        private_copy(binary, frozen)
        frozen.chmod(0o700)
        source = json.loads(subprocess.check_output([str(frozen), '-build-info'], text=True))
        report = outdir / f'preflight-{variant}.json'
        subprocess.run([str(frozen), '-snapshot', str(snapshot), '-cases', str(cases),
                        '-dry-run', '-output', str(report)], check=True)
        variants[variant] = {'root': str(root), 'source': source,
                             'binary': str(frozen), 'binarySha256': digest(frozen)}
        print(f'{variant}: offline preflight passed ({source["commit"][:8]})', flush=True)
    return variants


def stats(rows, name):
    values = [r.get('metrics', {}).get(name) for r in rows]
    known = sorted(v for v in values if v is not None)
    return {'available': len(known), 'total': len(rows),
            'sum': sum(known) if known else None,
            'mean': statistics.mean(known) if known else None,
            'p50': statistics.median(known) if known else None,
            'p95': known[math.ceil(.95 * len(known)) - 1] if known else None}


def aggregate(rows, cases, repetitions):
    counts = Counter(r['caseId'] for r in rows)
    incomplete = {c['id']: counts[c['id']] for c in cases if counts[c['id']] != repetitions}
    unexpected = sorted(set(counts) - {c['id'] for c in cases})
    score_counts = {}
    for name in SCORES:
        values = [r.get('scores', {}).get(name) for r in rows]
        score_counts[name] = {'passed': sum(v is True for v in values),
                              'scored': sum(v is not None for v in values)}
    failures = [{'caseId': r['caseId'], 'localAttempt': r.get('_attempt'),
                 'status': r['status'], 'scores': r.get('scores', {}),
                 'evidenceChecks': r.get('judge', {}).get('checks', {}),
                 'traceId': r.get('traceId'), 'report': r.get('_report')}
                for r in rows if r['status'] != 'succeeded'
                or any(value is False for value in r.get('scores', {}).values())
                or any(value.get('score') == 0 for value in r.get('judge', {}).get('checks', {}).values())]
    result = {'expectedRuns': len(cases) * repetitions, 'actualRuns': len(rows),
            'incompleteCases': incomplete, 'unexpectedCases': unexpected,
            'scores': score_counts, 'failures': failures,
            'metrics': {name: stats(rows, name) for name in METRICS}}
    if any('judge' in row for row in rows):
        judgements = [row.get('judge', {}) for row in rows]
        result['judge'] = {'statuses': dict(Counter(j.get('status', 'missing') for j in judgements)),
                           'scores': {}, 'metrics': {name: stats(judgements, name) for name in
                                                   ('promptTokens', 'completionTokens', 'durationMs')}}
        for name in (*DIMENSIONS, 'semantic_quality', 'quality'):
            values = [j.get('overall') if name == 'quality' else j.get('semanticOverall') if name == 'semantic_quality'
                      else j.get('dimensions', {}).get(name, {}).get('score') for j in judgements]
            result['judge']['scores'][name] = {'passed': sum(v == 1 for v in values),
                                               'scored': sum(v is not None for v in values)}
        result['judge']['checks'] = {}
        for name in ('card_evidence', 'reminder_consistency', 'tool_policy'):
            values = [j.get('checks', {}).get(name, {}).get('score') for j in judgements]
            result['judge']['checks'][name] = {'passed': sum(v == 1 for v in values),
                                              'scored': sum(v is not None for v in values)}
    return result


def summarize(outdir):
    manifest = json.loads((outdir / 'comparison.json').read_text())
    suite = json.loads((outdir / 'cases.json').read_text())
    protocol = json.loads((outdir / 'protocol.json').read_text())
    categories = {c['id']: c['category'] for c in protocol['cases']}
    summary = {'phase': manifest['phase'], 'variants': {},
               'note': f'Data origin: {protocol.get("dataOrigin", "unspecified")}. '
                       'Manual quality review and thresholds remain pending; historical AI replies are not ground truth.'}
    lines = ['# Tool search A/B/C comparison', '', summary['note'], '',
             '| Variant | Recorded / expected | Execution | Fixture coverage | Prompt tokens (known / runs) | Median / p95 ms |',
             '| --- | --- | --- | --- | --- | --- |']
    records = {}
    for variant, info in manifest['variants'].items():
        rows = []
        for path in sorted((outdir / variant).glob('*.json')):
            report = json.loads(path.read_text())
            if report.get('formatVersion') != 'replay-v1' or report.get('dryRun'):
                continue
            if report['suiteSha256'] != manifest['suiteSha256'] or report['snapshotSha256'] != manifest['snapshotSha256']:
                raise ValueError(f'{variant}: mixed suite/snapshot in {path}')
            if (report['source']['commit'] != info['source']['commit']
                    or report['source']['harnessSha256'] != info['source']['harnessSha256']):
                raise ValueError(f'{variant}: mixed source/harness in {path}')
            judgement = None
            judge_path = path.parent / 'judgements' / path.name
            if judge_path.exists():
                judged = json.loads(judge_path.read_text())
                if (judged.get('reportFile') != path.name or judged.get('reportSha256') != digest(path)
                        or judged.get('suiteSha256') != manifest['suiteSha256']
                        or judged.get('snapshotSha256') != manifest['snapshotSha256']
                        or len(report['results']) != 1
                        or judged['judge']['caseId'] != report['results'][0]['caseId']):
                    raise ValueError(f'{variant}: judge report provenance differs in {judge_path}')
                if manifest.get('judge') and any(judged['judge'].get(k) != v for k, v in manifest['judge'].items()):
                    raise ValueError(f'{variant}: mixed judge configuration in {judge_path}')
                judgement = judged['judge']
            elif manifest.get('judge'):
                judgement = {'status': 'missing', 'overall': None}
            rows.extend({**r, '_report': str(path), **({'judge': judgement} if judgement is not None else {})}
                        for r in report['results'])
        attempts = Counter()
        for row in rows:
            attempts[row['caseId']] += 1
            row['_attempt'] = attempts[row['caseId']]
            metrics = row.setdefault('metrics', {})
            prompt, completion = metrics.get('promptTokens'), metrics.get('completionTokens')
            metrics['totalTokens'] = prompt + completion if prompt is not None and completion is not None else None
            calls = metrics.get('modelCalls')
            metrics['modelCallCount'] = len(calls) if isinstance(calls, list) else None
        records[variant] = rows
        # Phoenix invokes the binary once per task; its local repetition is always 1.
        totals = aggregate(rows, suite['cases'], manifest['repetitions'])
        totals['categories'] = {}
        for category in sorted(set(categories.values())):
            selected = [c for c in suite['cases'] if categories[c['id']] == category]
            if selected:
                totals['categories'][category] = aggregate(
                    [r for r in rows if categories.get(r['caseId']) == category],
                    selected, manifest['repetitions'])
        summary['variants'][variant] = totals
        scores, metrics = totals['scores'], totals['metrics']
        def rate(name):
            v = scores[name]
            return f'{v["passed"]}/{v["scored"]}'
        tokens, latency = metrics['promptTokens'], metrics['durationMs']
        lines.append(f'| {variant} | {len(rows)} / {totals["expectedRuns"]} | '
                     f'{rate("execution_success")} | {rate("map_fixtures_complete")} | '
                     f'{tokens["sum"]} ({tokens["available"]}/{len(rows)}) | '
                     f'{latency["p50"]} / {latency["p95"]} |')
    baseline = summary['variants'].get('A')
    summary['relativeToA'] = {}
    for variant, totals in summary['variants'].items():
        if variant == 'A' or baseline is None:
            continue
        deltas = {}
        complete = all(not v['incompleteCases'] and not v['unexpectedCases'] for v in (baseline, totals))
        for name, field in [('totalTokens', 'mean'), ('durationMs', 'p50')]:
            before, after = baseline['metrics'][name], totals['metrics'][name]
            known = all(m['available'] == v['expectedRuns'] for m, v in [(before, baseline), (after, totals)])
            deltas[name + 'ChangeFraction'] = (after[field] / before[field] - 1
                if complete and known and before[field] is not None and before[field] > 0 else None)
        summary['relativeToA'][variant] = deltas
    lines += ['', '## By category', '',
              '| Category | Variant | Recorded / expected | Failed runs | Total tokens (known / runs) | Median ms | Mean searches | Mean cumulative input bytes |',
              '| --- | --- | --- | --- | --- | --- | --- | --- |']
    for variant, totals in summary['variants'].items():
        for category, values in totals['categories'].items():
            metrics = values['metrics']
            tokens = metrics['totalTokens']
            lines.append(f'| {category} | {variant} | {values["actualRuns"]} / {values["expectedRuns"]} | '
                         f'{len(values["failures"])} | {tokens["sum"]} ({tokens["available"]}/{values["actualRuns"]}) | '
                         f'{metrics["durationMs"]["p50"]} | {metrics["searchCalls"]["mean"]} | '
                         f'{metrics["cumulativeInputTokens"]["mean"]} |')
    lines += ['', 'Missing token usage is unavailable, never zero. Incomplete runs cannot support a winner.',
              'Per-category scores, metrics, failures and report paths are in summary.json.', '',
              '## Manual review', '',
              'Check every case/repetition against protocol.json, including failed runs. '
              'Tool names and keyword assertions alone do not establish semantic correctness.', '']
    judged_variants = {v: t['judge'] for v, t in summary['variants'].items() if 'judge' in t}
    if judged_variants:
        lines += ['', '## Semantic judge and evidence checks (auxiliary)', '',
                  '| Variant | Raw semantic pass / scored | Combined pass / scored | Status counts | Judge prompt / completion tokens |',
                  '| --- | --- | --- | --- | --- |']
        for variant, judged in judged_variants.items():
            quality = judged['scores']['quality']
            semantic = judged['scores']['semantic_quality']
            lines.append(f'| {variant} | {semantic["passed"]}/{semantic["scored"]} | '
                         f'{quality["passed"]}/{quality["scored"]} | {judged["statuses"]} | '
                         f'{judged["metrics"]["promptTokens"]["sum"]} / {judged["metrics"]["completionTokens"]["sum"]} |')
        lines += ['', 'Judge usage is separate from Agent usage. Missing/not_scored judgements are not passes. '
                  'Same-model judging needs human calibration; it does not establish an independent quality win.']
    review = ['# 运行结果人工复核', '', '每行对应一个预期执行；结合原报告、Trace、protocol.json逐项检查。', '',
              'Attempt为本地报告顺序编号，Phoenix每次单题执行的原始repetition字段均为1。', '',
              '| Variant | Case | Attempt | Quality pass/fail | Notes / report / Trace |',
              '| --- | --- | --- | --- | --- |']
    for variant in manifest['variants']:
        for case in suite['cases']:
            for rep in range(1, manifest['repetitions'] + 1):
                matches = [r for r in records[variant] if r['caseId'] == case['id'] and r['_attempt'] == rep]
                evidence = (f'{matches[0]["_report"]}; Trace={matches[0].get("traceId", "unavailable")}'
                            if matches else '缺少执行结果')
                review.append(f'| {variant} | {case["id"]} | {rep} | 待复核 | {evidence} |')
    generated_text(outdir / 'summary.json', json.dumps(summary, ensure_ascii=False, indent=2) + '\n')
    generated_text(outdir / 'report.md', '\n'.join(lines) + '\n')
    # Preserve human notes when an interrupted run is summarized again.
    if not (outdir / 'manual-review.md').exists():
        generated_text(outdir / 'manual-review.md', '\n'.join(review) + '\n')
    return summary


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=['check', 'run', 'summarize'])
    parser.add_argument('--phase', choices=['smoke', 'full'], default='full')
    parser.add_argument('--variants', choices=list(VARIANTS), nargs='+', default=list(VARIANTS),
                        help='variants to compare, including A; default A B C')
    parser.add_argument('--cases', type=Path, default=SUITE / 'cases.draft.json')
    parser.add_argument('--snapshot', type=Path, default=SUITE / 'messages.synthetic.jsonl')
    parser.add_argument('--protocol', type=Path, default=SUITE / 'protocol.json')
    for variant in VARIANTS:
        parser.add_argument(f'--{variant.lower()}-root', type=Path)
    parser.add_argument('--model-config', type=Path, default=ROOT / 'local/models.json')
    parser.add_argument('--env-file', type=Path, default=ROOT / 'local/.env')
    parser.add_argument('--model-profile')
    parser.add_argument('--judge-profile')
    parser.add_argument('--judge-timeout', type=int, default=60)
    parser.add_argument('--no-judge', action='store_true')
    parser.add_argument('--endpoint', default='http://127.0.0.1:16006')
    parser.add_argument('--case-timeout', type=int, default=300)
    parser.add_argument('--max-tasks', type=int, help='required for run; cap Agent executions, not model requests or currency')
    parser.add_argument('--output-dir', type=Path)
    args = parser.parse_args()
    if args.command == 'summarize':
        if not args.output_dir:
            parser.error('summarize requires --output-dir')
        summarize(args.output_dir.resolve())
        return
    if args.case_timeout <= 0 or args.judge_timeout <= 0:
        parser.error('timeouts must be positive')
    if args.no_judge and args.judge_profile:
        parser.error('--no-judge and --judge-profile are mutually exclusive')
    settings = evaluation_settings(ROOT)
    args.model_profile = args.model_profile or settings.get('modelProfile')
    judge_profile = None if args.no_judge else args.judge_profile or settings.get('judgeProfile')
    online = args.command == 'run'
    if 'A' not in args.variants or len(set(args.variants)) != len(args.variants):
        parser.error('--variants must include A without duplicates')
    suite, protocol = load_inputs(args.cases, args.snapshot, args.protocol, args.phase, online)
    repetitions = 1 if args.phase == 'smoke' else protocol['repetitions']
    if not isinstance(repetitions, int) or not 1 <= repetitions <= 20:
        raise ValueError('protocol repetitions must be 1-20')
    tasks = len(args.variants) * len(suite['cases']) * repetitions
    if online and (not args.model_profile or args.max_tasks is None or args.max_tasks < tasks):
        raise ValueError(f'run requires --model-profile and --max-tasks >= {tasks}; '
                         'a task may make multiple model requests')
    roots = source_roots({v: getattr(args, v.lower() + '_root') for v in VARIANTS},
                         protocol['baselineCommit'], protocol.get('variantCommits'), args.variants)
    config = args.model_config.resolve()
    if online:
        model_config = json.loads(config.read_text())
        models = model_config.get('models', {})
        if args.model_profile not in models:
            raise ValueError('model-profile does not exist in model-config')
    judge = (ReplayJudge(model_config, judge_profile, model_environment(ROOT, args.env_file.resolve()),
                         args.judge_timeout) if online and judge_profile else None)
    outdir = args.output_dir.resolve() if args.output_dir else ROOT / 'outputs/evals/tool-search' / (
        datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%SZ-') + uuid.uuid4().hex[:8])
    outdir.mkdir(parents=True, exist_ok=False, mode=0o700)
    private_json(outdir / 'cases.json', suite)
    private_json(outdir / 'protocol.json', protocol)
    private_copy(args.snapshot, outdir / 'messages.jsonl')
    if online:
        private_copy(config, outdir / 'models.json')
    snapshot, cases = outdir / 'messages.jsonl', outdir / 'cases.json'
    variants = preflight(roots, snapshot, cases, outdir)
    harnesses = {v['source']['harnessSha256'] for v in variants.values()}
    if len(harnesses) != 1:
        raise ValueError('A/B/C harness hashes differ')
    manifest = {'phase': args.phase, 'mode': args.command, 'suiteSha256': digest(cases),
                'selectedVariants': args.variants,
                'snapshotSha256': digest(snapshot), 'repetitions': repetitions,
                'protocolSha256': digest(outdir / 'protocol.json'),
                'plannedTasks': tasks, 'variants': variants, 'modelProfile': args.model_profile,
                'judge': judge.metadata if judge else None,
                'modelConfigSha256': digest(outdir / 'models.json') if online else None,
                'toolingSha256': {name: digest(ROOT / 'scripts' / name) for name in
                                 ('tool_search_eval.py', 'eval_replay.py', 'phoenix_replay.py',
                                  'replay_judge.py', 'replay_checks.py')}}
    private_json(outdir / 'comparison.json', manifest)
    print(f'{tasks} planned tasks; reports: {outdir}', flush=True)
    if not online:
        print('Offline only. No model, Phoenix, business DB or map service was contacted.')
        return
    failed = False
    try:
        for variant, info in variants.items():
            frozen_inputs = {'cases.json': manifest['suiteSha256'],
                             'messages.jsonl': manifest['snapshotSha256'],
                             'models.json': manifest['modelConfigSha256'],
                             'protocol.json': manifest['protocolSha256']}
            if any(digest(outdir / name) != expected for name, expected in frozen_inputs.items()):
                raise ValueError('frozen comparison input changed')
            if digest(Path(info['binary'])) != info['binarySha256']:
                raise ValueError('frozen binary changed')
            for name, expected in manifest['toolingSha256'].items():
                if digest(ROOT / 'scripts' / name) != expected:
                    raise ValueError('evaluation tooling changed during comparison')
            command = [sys.executable, str(ROOT / 'scripts/phoenix_replay.py'),
                       '--source-root', info['root'], '--binary', info['binary'],
                       '--snapshot', str(snapshot), '--cases', str(cases),
                       '--model-config', str(outdir / 'models.json'),
                       '--model-profile', args.model_profile, '--env-file', str(args.env_file.resolve()),
                       '--endpoint', args.endpoint, '--repetitions', str(repetitions),
                       '--case-timeout', str(args.case_timeout),
                       '--experiment', f'tool-search-{variant}-{args.phase}-{outdir.name}',
                       '--output-dir', str(outdir / variant)]
            command += (['--judge-profile', judge_profile, '--judge-timeout', str(args.judge_timeout)]
                        if judge else ['--no-judge'])
            subprocess.run(command, cwd=ROOT, check=True)
    except subprocess.CalledProcessError:
        failed = True
    finally:
        summary = summarize(outdir)
    failed = failed or any(v['incompleteCases'] or v['unexpectedCases'] or v['failures']
                           or any(v.get('judge', {}).get('statuses', {}).get(s, 0) for s in ('error', 'missing'))
                           for v in summary['variants'].values())
    if failed:
        raise ValueError(f'comparison has failures or missing runs; inspect {outdir / "report.md"}')


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        print('Tool search evaluation failed: ' + str(error), file=sys.stderr)
        sys.exit(1)

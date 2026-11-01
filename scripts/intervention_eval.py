#!/usr/bin/env python3
"""Compare production Discover on paired 15/30-message windows; private reports only."""
import argparse
from collections import Counter
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import statistics
import subprocess
import tarfile
import uuid

from dev import load_env

ROOT = Path(__file__).resolve().parents[1]


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def private_json(path, data):
    with os.fdopen(os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600), 'w') as f:
        json.dump(data, f, ensure_ascii=False, indent=2)
        f.write('\n')


def source_files(root):
    files = [p for p in (root / 'services/api').rglob('*')
             if p.is_file() and (p.suffix in {'.go', '.md'} or p.name in {'go.mod', 'go.sum'})]
    files += [root / 'scripts/intervention_eval.py', root / 'scripts/dev.py']
    return sorted(files)


def aggregate(rows, cases):
    """Never present unreviewed labels as formal quality scores."""
    result = {}
    for size in [15, 30]:
        selected = [r for r in rows if r['window'] == size]
        good = [r for r in selected if r['status'] == 'succeeded']
        negative = [r for r in good if cases[r['caseId']]['reference']['productAction'] == 'skip']
        positive = [r for r in good if cases[r['caseId']]['reference']['productAction'] == 'intervene']
        calls = [c for r in selected for c in r['modelCalls']]
        def total(field):
            return sum(c[field] for c in calls) if calls and all(c.get(field) is not None for c in calls) else None
        def agreement(items):
            return {'matches': sum(r['decision']['action'] == cases[r['caseId']]['windowReferences'][str(size)]['action'] for r in items), 'executions': len(items)}
        reviewed = [r for r in good if cases[r['caseId']]['review'].get('reviewed') is True]
        result[str(size)] = {
            'attempted': len(selected), 'succeeded': len(good), 'failed': len(selected) - len(good),
            'actions': dict(Counter(r['decision']['action'] for r in good)),
            'reviewedQuality': agreement(reviewed) if reviewed else None,
            'exploratoryOnly': {
                'windowActionAgreement': agreement(good),
                'negativeCases': {'succeeded': len(negative), 'falseInterventions': sum(r['decision']['action'] == 'intervene' for r in negative), 'verify': sum(r['decision']['action'] == 'verify' for r in negative)},
                'positiveCases': {'succeeded': len(positive), 'directInterventions': sum(r['decision']['action'] == 'intervene' for r in positive), 'verify': sum(r['decision']['action'] == 'verify' for r in positive), 'skip': sum(r['decision']['action'] == 'skip' for r in positive)},
                'byOrigin': {origin: agreement([r for r in good if cases[r['caseId']]['dataOrigin'] == origin]) for origin in sorted({cases[r['caseId']]['dataOrigin'] for r in good})},
                'byCategory': {cat: agreement([r for r in good if cases[r['caseId']]['category'] == cat]) for cat in sorted({cases[r['caseId']]['category'] for r in good})},
            },
            'modelCalls': len(calls), 'promptTokens': total('promptTokens'), 'completionTokens': total('completionTokens'),
            'providerUsageComplete': bool(calls) and all(c.get('promptTokens') is not None and c.get('completionTokens') is not None for c in calls),
            'meanDurationMs': round(statistics.mean(r['durationMs'] for r in good), 1) if good else None,
            'medianDurationMs': statistics.median(r['durationMs'] for r in good) if good else None,
        }
    return result


def markdown(summary, rows, cases, run_dir):
    windows = summary['windows']
    lines = ['# 主动介入 15／30 条窗口首轮对照\n',
             '本轮为探索性识别实验。参考尚未人工审核；数字用于排查差异，不是正式准确率或生产验收结论。\n',
             f"分支：`{summary['source']['branch']}`；基线：`{summary['source']['commit']}`。\n",
             '仅调用生产 Discover 判断器，未改提示词、未修改生产窗口、未执行历史核验或主 Agent；未发送群消息、未上传 Phoenix。\n',
             '| 指标 | 15条 | 30条 |\n|---|---:|---:|\n']
    for title, key in [('执行成功', 'succeeded'), ('执行失败', 'failed'), ('模型调用数（含格式修复）', 'modelCalls'), ('输入token', 'promptTokens'), ('输出token', 'completionTokens'), ('中位耗时ms', 'medianDurationMs')]:
        lines.append(f"| {title} | {windows['15'][key]} | {windows['30'][key]} |\n")
    for size in [15, 30]:
        w = windows[str(size)]
        ex = w['exploratoryOnly']
        lines.append(f"\n{size}条：局部窗口预期匹配 {ex['windowActionAgreement']['matches']}/{w['succeeded']}；12个静默目标中的直接介入 {ex['negativeCases']['falseInterventions']}/{ex['negativeCases']['succeeded']}；正例直接介入 {ex['positiveCases']['directInterventions']}/{ex['positiveCases']['succeeded']}，verify {ex['positiveCases']['verify']}，skip {ex['positiveCases']['skip']}。失败执行另列，不混入成功分母。\n")
    lines += ['\n## 逐例输出\n', '| 案例 | 来源 | 产品目标 | 15条实际 / 局部预期 | 30条实际 / 局部预期 |\n|---|---|---|---|---|\n']
    for cid, c in cases.items():
        values = []
        for size in [15, 30]:
            found = [r for r in rows if r['caseId'] == cid and r['window'] == size]
            actual = ', '.join(r['decision']['action'] if r['status'] == 'succeeded' else 'ERROR' for r in found) or '未执行'
            values.append(f"{actual} / {c['windowReferences'][str(size)]['action']}")
        origin = '真实' if c['dataOrigin'] == 'production-chat-export' else '合成'
        lines.append(f"| {cid} {c['name']} | {origin} | {c['reference']['productAction']} | {values[0]} | {values[1]} |\n")
    lines += ['\n## 差异与失败详情\n']
    for cid, c in cases.items():
        found = [r for r in rows if r['caseId'] == cid]
        if any(r['status'] != 'succeeded' or r['decision']['action'] != c['windowReferences'][str(r['window'])]['action'] for r in found) or c['boundary']['decisive']:
            lines.append(f"\n### {cid} {c['name']}\n")
            for r in sorted(found, key=lambda r: (r['window'], r['repetition'])):
                reason = r.get('error') or r['decision'].get('reason', '')
                lines.append(f"\n- {r['window']}条，第{r['repetition']}次：{r['status']} / {r['decision'].get('action', '')}。{reason}\n")
    lines += ['\n## 适用范围\n',
              '\n3例真实聊天均为静默目标，17例合成场景包含全部正例。真实窗口部分重叠，已有开发用例派生场景不属于独立留出集。不能据此推断生产误打扰率。\n',
              '\nverify单独统计，既不视为已介入，也不视为最终静默。缩窗丢失拒绝时，即使局部判断有依据，仍保留产品误打扰计数。每种窗口首轮只执行一次，尚未验证稳定性。\n',
              f'\n原始请求、响应、源码及配置快照保存在本报告所在私有目录：`{run_dir}`。\n']
    return ''.join(lines)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--cases', type=Path, required=True)
    parser.add_argument('--output-dir', type=Path)
    parser.add_argument('--repetitions', type=int, default=1)
    parser.add_argument('--seed', type=int, default=20261101)
    parser.add_argument('--case-timeout', default='90s')
    parser.add_argument('--model-config', type=Path, default=ROOT / 'local/models.json')
    parser.add_argument('--model-profile')
    parser.add_argument('--dry-run', action='store_true')
    args = parser.parse_args()
    if not 1 <= args.repetitions <= 20:
        parser.error('repetitions must be 1-20')
    dataset = json.loads(args.cases.read_text())
    case_map = {c['id']: c for c in dataset['cases']}
    if len(case_map) != len(dataset['cases']):
        parser.error('duplicate case IDs')
    # Capture exact source bytes, including uncommitted harness changes.
    files = source_files(ROOT)
    hashes = {str(p.relative_to(ROOT)): digest(p) for p in files}
    source = {'branch': subprocess.check_output(['git', 'branch', '--show-current'], cwd=ROOT, text=True).strip(),
              'commit': subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip(),
              'dirty': bool(subprocess.check_output(['git', 'status', '--porcelain'], cwd=ROOT, text=True).strip())}
    run_dir = args.output_dir or ROOT / 'outputs/evals/intervention' / (datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%SZ-') + uuid.uuid4().hex[:8])
    run_dir = run_dir.resolve()
    run_dir.mkdir(parents=True, mode=0o700, exist_ok=False)
    os.chmod(run_dir, 0o700)
    with os.fdopen(os.open(run_dir / 'cases.json', os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600), 'wb') as f:
        f.write(args.cases.read_bytes())
    archive = run_dir / 'source.tar.gz'
    with tarfile.open(archive, 'x:gz') as tar:
        for path in files:
            tar.add(path, arcname=str(path.relative_to(ROOT)), recursive=False)
    archive.chmod(0o600)
    binary = run_dir / 'eval-intervention'
    subprocess.run(['go', 'build', '-trimpath', '-o', str(binary), './cmd/eval-intervention'], cwd=ROOT / 'services/api', check=True)
    command = [str(binary), '-cases', str(run_dir / 'cases.json'), '-output', str(run_dir / 'results.json'),
               '-seed', str(args.seed), '-repetitions', str(args.repetitions), '-case-timeout', args.case_timeout]
    env = os.environ.copy()
    model_profile = None
    if args.dry_run:
        command += ['-dry-run']
    else:
        settings_path = ROOT / 'local/eval-settings.json'
        settings = json.loads(settings_path.read_text()) if settings_path.exists() else {}
        profiles = json.loads(args.model_config.read_text())
        model_profile = args.model_profile or settings.get('modelProfile') or profiles['default_profile']
        config = profiles['models'][model_profile]
        # Model configs contain public connection settings and API-key env names, never key values.
        allowed = {'provider', 'base_url', 'model', 'api_key_env', 'output_mode', 'enable_thinking', 'context_tokens'}
        if set(config) - allowed:
            raise ValueError('unsupported fields in selected model config')
        private_json(run_dir / 'models.json', {'default_profile': model_profile, 'models': {model_profile: config}})
        load_env(ROOT / 'local/.env', env, set(env))
        command += ['-model-config', str(run_dir / 'models.json'), '-model-profile', model_profile]
    for key in list(env):
        if key.startswith('JETAIME_PHOENIX_') or key in {'JETAIME_DATABASE_URL', 'JETAIME_TEST_DATABASE_URL', 'JETAIME_GAODE_API_KEY', 'JETAIME_EVAL_TRACEPARENT'}:
            env.pop(key, None)
    private_json(run_dir / 'manifest.json', {'version': 'intervention-eval-run-v1', 'source': source,
                 'sourceSha256': hashes, 'sourceArchiveSha256': digest(archive), 'binarySha256': digest(binary),
                 'suiteSha256': digest(run_dir / 'cases.json'), 'modelProfile': model_profile,
                 'modelConfigSha256': digest(run_dir / 'models.json') if not args.dry_run else None,
                 'seed': args.seed, 'repetitions': args.repetitions, 'plannedExecutions': len(case_map)*2*args.repetitions,
                 'dryRun': args.dry_run, 'referenceStatus': 'pending_human_review' if any(not c['review']['reviewed'] for c in case_map.values()) else 'reviewed'})
    # Log contains only case IDs/actions/status; full bodies stay in private results.
    with os.fdopen(os.open(run_dir / 'progress.log', os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600), 'w') as log:
        completed = subprocess.run(command, cwd=ROOT / 'services/api', env=env, stderr=log)
    changed = [name for name, sha in hashes.items() if digest(ROOT / name) != sha]
    if changed:
        raise RuntimeError('source changed during build/run; result not comparable: ' + ', '.join(changed))
    if digest(run_dir / 'cases.json') != digest(args.cases):
        raise RuntimeError('input suite changed during run')
    if not (run_dir / 'results.json').exists() or not (run_dir / 'results.json').stat().st_size:
        raise RuntimeError('run failed before report; inspect private progress.log')
    report = json.loads((run_dir / 'results.json').read_text())
    if report['dryRun']:
        print(json.dumps({'runDirectory': str(run_dir), 'dryRun': True, 'plannedExecutions': report['plannedExecutions']}, ensure_ascii=False))
    else:
        summary = {'version': 'intervention-window-summary-v1', 'source': source, 'completed': report['completed'],
                   'referenceStatus': 'provisional' if any(not c['review']['reviewed'] for c in case_map.values()) else 'reviewed',
                   'windows': aggregate(report['results'], case_map)}
        private_json(run_dir / 'summary.json', summary)
        with os.fdopen(os.open(run_dir / 'report.md', os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600), 'w') as f:
            f.write(markdown(summary, report['results'], case_map, run_dir))
        print(json.dumps({'runDirectory': str(run_dir), 'completed': report['completed'], 'windows': summary['windows']}, ensure_ascii=False))
    return completed.returncode


if __name__ == '__main__':
    raise SystemExit(main())

#!/usr/bin/env python3
"""Render actual outputs alongside unreviewed references, without quality pass rates."""
import argparse
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
p = argparse.ArgumentParser()
p.add_argument('results', type=Path)
p.add_argument('--cases', type=Path, required=True, help='The frozen cases used by this run; never silently use the current working draft')
p.add_argument('--output', type=Path, default=ROOT/'outputs/evals/reports/report.md')
args = p.parse_args()
cases = {c['id']: c for c in json.loads(args.cases.read_text())}
rows = [json.loads(line) for line in args.results.read_text().splitlines() if line]
lines = ['# 导入聊天实测结果', '', '真实模型输出与待审参考对照。参考未经过人工审核；差异仅供排查，不是正式质量评分。', '',
         f'本次记录 {len(rows)} 条；模型：' + ', '.join(sorted({r['output'].get('run',{}).get('model','unknown') for r in rows})), '',
         '| 案例 | 执行 | 引用格式检查 | 草稿断言对照 | 输入 / 输出 Token | 耗时（秒） |', '| --- | --- | --- | --- | --- | --- |']
for r in rows:
    c = cases[r['caseId']]; run = r['output'].get('run', {}); expected = c['expected']; diffs = []
    actual = run.get('answer', {})
    if 'answerStatus' in expected and actual.get('status') != expected['answerStatus']:
        diffs.append('状态不同')
    if 'suggestionCount' in expected and len(run.get('suggestions', [])) != expected['suggestionCount']:
        diffs.append('数量不同')
    sources = set(actual.get('sourceMessageIds', []))
    for s in run.get('suggestions', []): sources.update(s.get('sourceMessageIds', []))
    if not set(expected.get('requiredSourceMessageIds', [])) <= sources:
        diffs.append('未含全部草稿指定引用')
    model_calls = [s.get('output', {}) for s in run.get('steps', []) if s['name'].endswith('model_call')]
    model = {key: sum(call[key] for call in model_calls) if model_calls and all(isinstance(call.get(key), int) for call in model_calls) else '?' for key in ['promptTokens', 'completionTokens']}
    integrity = r.get('evaluations',{}).get('source_integrity', {}).get('label','unknown')
    lines.append(f"| {r['caseId']} | {run.get('status', 'failed')} | {integrity} | {'、'.join(diffs) or '无配置断言差异（语义待审）'} | {model.get('promptTokens', '?')} / {model.get('completionTokens', '?')} | {run.get('durationMs', 0)/1000:.2f} |")
for r in rows:
    c = cases[r['caseId']]; run = r['output'].get('run', {})
    lines += ['', '## '+r['caseId'], '', c['input'].get('question','提取截至片段末尾的待办'), '', '**真实输出**', '', '```json',
              json.dumps(run.get('answer', run.get('suggestions', [])), ensure_ascii=False, indent=2), '```', '',
              '**待审参考**', '', '```json', json.dumps(c.get('expected', {}).get('reference', {}), ensure_ascii=False, indent=2), '```', '',
              ('Trace ID：`'+r['taskTraceId']+'`' if r.get('taskTraceId') else '运行 ID：`'+r['experimentRunId']+'`')]
    for step in run.get('steps', []):
        if step['name'] == 'review_actions':
            lines += ['', '**候选行动状态核对（模型判断）**', '', '```json', json.dumps(step['output'], ensure_ascii=False, indent=2), '```']
args.output.parent.mkdir(parents=True, exist_ok=True)
args.output.write_text('\n'.join(lines)+'\n')
print(args.output)

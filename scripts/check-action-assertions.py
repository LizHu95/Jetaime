#!/usr/bin/env python3
"""Recheck saved outputs under a new rubric without rewriting experiment scores."""
import json
from pathlib import Path
import sys

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT/'evals'))
from runner import load_cases
from evaluators import reference_rules


def main():
    cases, digest = load_cases(ROOT/'local/data/evals/versions/chat-v1.1-reviewed-r2/cases.json')
    by = {c['id']: c for c in cases}
    old = {c['id']: c for c in json.loads((ROOT/'local/data/evals/versions/chat-v1.1-reviewed-r1/cases.json').read_text())}
    assert all(c['input']==old[c['id']]['input'] for c in cases)
    report = {'mode': 'offline retrospective rule checks; original scores unchanged; no LLM calls',
              'referenceVersion': 'chat-v1.1-reviewed-r2', 'casesSha256': digest, 'runs': []}
    for suffix in ['0fabbf16','8e938583','36172e7d','4bc8e35c']:
        rows = [json.loads(l) for l in (ROOT/f'local/data/evals/output/experiment-{suffix}.jsonl').read_text().splitlines()]
        checks = []
        for row in rows:
            c = by.get(row['caseId'])
            if not c or c['input']['kind']!='extract':
                continue
            assert row['output']['run']['groupId']==c['input']['groupId']
            checks.append({'caseId':c['id'],'repetition':row['repetition'],
                'oldReferenceCheck': reference_rules(c['input'],row['output'],old[c['id']]['expected']),
                'newReferenceCheck': reference_rules(c['input'],row['output'],c['expected'])})
        failures = [c for c in checks if c['newReferenceCheck']['label']=='fail']
        report['runs'].append({'runId':suffix,'checked':len(checks),'failed':len(failures),'checks':checks})
    # Known regression fixtures must fail; the previous v4 outputs must still pass.
    runs = {r['runId']:r for r in report['runs']}
    assert runs['8e938583']['failed']==0
    assert runs['36172e7d']['failed']==3
    assert runs['4bc8e35c']['failed']==2
    cache = json.loads((ROOT/'local/data/chat-eval/review-cache-check.json').read_text())
    cache_checks=[]
    for case in cache['cases']:
        c=by[case['caseId']]
        for row in case['runs']:
            cache_checks.append({'caseId':c['id'],'repetition':row['repetition'],
                'check':reference_rules(c['input'],{'run':row['run']},c['expected'])})
    report['cacheValidationRecheck'] = cache_checks
    assert sum(c['check']['label']=='fail' for c in cache_checks)==3
    output = ROOT/'local/data/chat-eval/action-assertions-r2.json'
    output.write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n')
    lines=['# reviewed-r2：行动漏判断言验证','','基于reviewed-r1强化参考，消息输入不变。这里只重放已保存输出，不调用模型、不修改Phoenix实验或旧评分，也不是新版本模型准确率。','','新增逐行动匹配：标题必须包含配置词、负责人一致、时间（配置时）一致、每项来源匹配；每个必需行动对应不同建议。另配置禁止行动。关键词匹配是确定性检查，不能涵盖全部同义表达或替代语义裁判。','','| 历史run | 检查的提取输出 | 新规则失败 |','| --- | --- | --- |']
    for r in report['runs']:lines.append(f"| {r['runId']} | {r['checked']} | {r['failed']} |")
    lines+=['','v4完整复测的18条提取输出仍通过；v5漏掉骑回家的3次均失败，v6漏掉骑回家的2次均失败。缓存验证里的3次uncertain过滤也被新规则识别为漏提取。规则因此能发现之前执行/数量/引用合法但行动内容错误的问题。','','## 失败依据','']
    for r in report['runs']:
        for c in r['checks']:
            if c['newReferenceCheck']['label']=='fail':
                lines.append(f"- {r['runId']} / {c['caseId']} / 第{c['repetition']}次：{c['newReferenceCheck']['explanation']}")
    lines+=['','所有旧案例包与实验保留，工作稿及新冻结版本指向reviewed-r2。后续真实模型复测应显式选择新案例版本。']
    output.with_suffix('.md').write_text('\n'.join(lines)+'\n')
    print(json.dumps([{'runId':r['runId'],'checked':r['checked'],'failed':r['failed']} for r in report['runs']],ensure_ascii=False))
    print(output.with_suffix('.md'))


if __name__=='__main__':
    main()

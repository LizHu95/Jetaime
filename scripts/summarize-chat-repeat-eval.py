#!/usr/bin/env python3
"""Compare repeat runs against an unchanged frozen bundle; no semantic pass rate."""
import argparse
import collections
import json
import statistics
from pathlib import Path
from artifact_paths import relocated

ROOT = Path(__file__).resolve().parents[1]
p=argparse.ArgumentParser()
p.add_argument('summary',type=Path)
p.add_argument('--baseline',type=Path,default=ROOT/'outputs/evals/experiment-0fabbf16.jsonl')
p.add_argument('--output',type=Path,default=ROOT/'outputs/evals/reports/repeat3-report.md')
a=p.parse_args()


summary=json.loads(relocated(a.summary).read_text())
snapshot=relocated(summary['snapshot'])
cases=json.loads((snapshot/'cases.json').read_text())
rows=[json.loads(l) for l in relocated(summary['results']).read_text().splitlines() if l]
baseline=[json.loads(l) for l in relocated(a.baseline).read_text().splitlines() if l]
assert len(rows)==len(cases)*summary['repetitions'], 'Incomplete run'
by=collections.defaultdict(list)
for row in rows:by[row['caseId']].append(row)

def normalized(row):
 run=row['output'].get('run',{})
 if run.get('kind')=='qa':return run.get('answer')
 return [{k:s.get(k) for k in ['title','assigneeId','timeExpression','sourceMessageIds']} for s in run.get('suggestions',[])]

def counts(row):
 calls=[s for s in row['output'].get('run',{}).get('steps',[]) if s['name'].endswith('model_call')]
 return {'calls':len(calls),'promptTokens':sum(s.get('output',{}).get('promptTokens') or 0 for s in calls),
         'completionTokens':sum(s.get('output',{}).get('completionTokens') or 0 for s in calls),
         'reviewDurationMs':sum(s.get('durationMs',0) for s in calls if s['name']=='review_model_call'),
         'durationMs':row['output'].get('run',{}).get('durationMs',0)}

def totals(items):
 vals=[counts(r) for r in items]
 return {'runs':len(items),'modelCalls':sum(v['calls'] for v in vals),'promptTokens':sum(v['promptTokens'] for v in vals),
         'completionTokens':sum(v['completionTokens'] for v in vals),'meanDurationSeconds':statistics.mean(v['durationMs'] for v in vals)/1000,
         'medianDurationSeconds':statistics.median(v['durationMs'] for v in vals)/1000,
         'meanTokensPerRun':statistics.mean(v['promptTokens']+v['completionTokens'] for v in vals),
         'meanReviewDurationSeconds':statistics.mean(v['reviewDurationMs'] for v in vals)/1000}

observations=[]
for case in cases:
 rs=by[case['id']]
 variants=collections.Counter(json.dumps(normalized(r),ensure_ascii=False,sort_keys=True) for r in rs)
 observations.append({'caseId':case['id'],'kind':case['input']['kind'],'question':case['input'].get('question','提取待办'),
                      'repetitions':len(rs),'distinctOutputs':len(variants),'outputs':[{'count':n,'output':json.loads(out)} for out,n in variants.items()],
                      'traceIds':[r['taskTraceId'] for r in rs if r.get('taskTraceId')],'performance':totals(rs)})

checks={}
gym=by['extract-explicit-schedule']
checks['gym_and_haircut_correct_subject']=[len(normalized(r))==2 and all(s['assigneeId']=='wechat-l' and 'wx-0165' in s['sourceMessageIds'] for s in normalized(r)) and any('健身' in s['title'] and s['timeExpression']=='18:00-19:00' for s in normalized(r)) and any('理发' in s['title'] and s['timeExpression']=='20:00' for s in normalized(r)) for r in gym]
checks['invitation_not_extracted']=[normalized(r)==[] for r in by['extract-exam-invitation']]
checks['completed_purchase_excluded']=[not any('面包' in s['title'] for s in normalized(r)) for r in by['extract-completed-purchase']]
checks['future_english_preserved']=[any('英语' in s['title'] for s in normalized(r)) for r in by['extract-completed-purchase']]
result={'suiteVersion':summary['metadata']['suiteVersion'],'experimentId':summary['experimentId'],'url':summary.get('url'),
        'metrics':summary['metrics'],'metadata':summary['metadata'],'current':totals(rows),'baseline':totals(baseline),
        'draftRegressionObservations':checks,'cases':observations,'officialSemanticScore':None}
a.output.parent.mkdir(parents=True,exist_ok=True)
a.output.with_suffix('.json').write_text(json.dumps(result,ensure_ascii=False,indent=2)+'\n')
lines=['# 固定聊天评测集：三次重复运行', '',f"案例包：{result['suiteVersion']}；23案例×3次，共{len(rows)}条运行。", '',
       '原始案例和参考没有修改；参考仍待人工审核。以下稳定性及规则对照不是正式语义通过率。输入为局部聊天片段，不代表全量历史检索效果。', '',
       (f"[历史实验]({summary['url']})；源码和配置快照：`{snapshot}`。" if summary.get("url") else f"本地实验：`{summary['experimentId']}`；源码和配置快照：`{snapshot}`。"), '',
       '## 执行与稳定性', '', '```json',json.dumps(summary['metrics'],ensure_ascii=False,indent=2),'```', '',
       f"{sum(o['distinctOutputs']==1 for o in observations)}/{len(observations)}个案例的三次输出完全一致（比较回答文字、状态、来源及建议内容，忽略运行生成的建议ID）。文字不同不自动代表事实错误；相同也不自动代表正确。", '',
       '## 已知问题对照（待审规则观察）', '', '```json',json.dumps(checks,ensure_ascii=False,indent=2),'```', '',
       '## 开销对照', '', '| 类型 | 版本 | 调用次数/运行 | 平均输入+输出Token/运行 | 平均耗时/秒 | 中位耗时/秒 |', '| --- | --- | --- | --- | --- | --- |']
for kind in ['qa','extract']:
 for label,items in [('原23条单次基线',[r for r in baseline if r['output'].get('run',{}).get('kind')==kind]),('当前三次复测',[r for r in rows if r['output'].get('run',{}).get('kind')==kind])]:
  t=totals(items)
  lines.append(f"| {kind} | {label} | {t['modelCalls']/t['runs']:.2f} | {t['meanTokensPerRun']:.1f} | {t['meanDurationSeconds']:.2f} | {t['medianDurationSeconds']:.2f} |")
lines+=['','耗时包含当次模型缓存、加载和机器负载影响；基线每例一次，不能据此作严格性能提升/退步结论。Token累计包含候选生成和状态核对两次调用。','','## 逐题实际输出','']
for o in observations:
 lines += [f"### {o['caseId']}",'',o['question'],'',f"三次得到{o['distinctOutputs']}种输出；平均耗时{o['performance']['meanDurationSeconds']:.2f}秒。",'']
 for variant in o['outputs']:
  lines+= [f"出现{variant['count']}次：",'','```json',json.dumps(variant['output'],ensure_ascii=False,indent=2),'```','']
 lines += ['Trace IDs：'+', '.join('`'+i+'`' for i in o['traceIds']),'']
a.output.write_text('\n'.join(lines)+'\n')
print(json.dumps({'report':str(a.output),'current':result['current'],'baseline':result['baseline'],'checks':checks,'stableCases':sum(o['distinctOutputs']==1 for o in observations)},ensure_ascii=False,indent=2))

#!/usr/bin/env python3
"""Freeze an immutable case bundle with independent input/reference fingerprints."""
import argparse
import hashlib
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
p = argparse.ArgumentParser()
p.add_argument('--cases', type=Path, default=ROOT/'local/data/evals/cases.json')
p.add_argument('--version', required=True)
p.add_argument('--data-version', required=True)
p.add_argument('--reference-version', required=True)
p.add_argument('--parent')
p.add_argument('--change', action='append', required=True)
a = p.parse_args()
if not a.version or any(ch not in 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-._' for ch in a.version) or a.version in ['.', '..']:
    p.error('version must be a simple directory name')
raw = a.cases.read_bytes()
cases = json.loads(raw)
if not cases or len({c['id'] for c in cases}) != len(cases): p.error('cases must be nonempty with unique IDs')
canonical = lambda value: json.dumps(value,ensure_ascii=False,sort_keys=True,separators=(',',':')).encode()
sha = lambda value: hashlib.sha256(value).hexdigest()
manifest = {'suiteVersion':a.version,'dataVersion':a.data_version,'referenceVersion':a.reference_version,'parent':a.parent,
            'changes':a.change,'caseCount':len(cases),'caseIds':[c['id'] for c in cases],
            'casesSha256':sha(raw),'inputSha256':sha(canonical([{'id':c['id'],'input':c['input']} for c in cases])),
            'referenceSha256':sha(canonical([{'id':c['id'],'expected':c.get('expected',{})} for c in cases])),
            'reviewedCount':sum(c.get('expected',{}).get('reviewed',False) for c in cases)}
folder = ROOT/'local/data/evals/versions'/a.version
# mkdir without exist_ok prevents silent replacement of frozen inputs/references.
folder.mkdir(parents=True, exist_ok=False)
(folder/'cases.json').write_bytes(raw)
(folder/'manifest.json').write_text(json.dumps(manifest,ensure_ascii=False,indent=2)+'\n')
print(folder)

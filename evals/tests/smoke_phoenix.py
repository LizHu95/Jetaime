"""Protocol-only integration check. Synthetic model, never a product evaluation."""
import argparse
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import sys
import tempfile
import threading
import time
from types import SimpleNamespace

import httpx
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from runner import ROOT, run_experiment, load_cases

class TestModel(BaseHTTPRequestHandler):
    def log_message(self, *args): pass
    def do_GET(self):
        self.send({'models': [{'name': 'protocol-test-model'}]})
    def do_POST(self):
        request = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        system = request['messages'][0]['content']
        if '独立的群组协同 Workflow 评估员' in system:
            result = {'label': 'pass', 'explanation': '仅为测试协议的固定响应，不代表产品效果'}
        elif '提取尚未创建' in system:
            result = {'suggestions': []}
        else:
            result = {'status': 'answered', 'answer': '仅为协议测试', 'sourceMessageIds': ['m2']}
        self.send({'done': True, 'done_reason': 'stop', 'message': {'content': json.dumps(result)}, 'prompt_eval_count': 1, 'eval_count': 1})
    def send(self, data):
        self.send_response(200); self.send_header('Content-Type', 'application/json'); self.end_headers(); self.wfile.write(json.dumps(data).encode())


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--phoenix-url', default='http://127.0.0.1:6006')
    options = parser.parse_args()
    server = ThreadingHTTPServer(('127.0.0.1', 0), TestModel)
    thread = threading.Thread(target=server.serve_forever, daemon=True); thread.start()
    try:
        with tempfile.TemporaryDirectory(prefix='jetaime-protocol-test-') as directory:
            fixture = json.loads((ROOT/'services/api/testdata/assistant/history.json').read_text())
            case = {'id': 'protocol-only', 'input': {'fixture': fixture, 'groupId':'daily','actorId':'lin','kind':'qa','question':'协议验证'},
                    'expected': {'reviewed': True, 'answerStatus':'answered', 'requiredSourceMessageIds':['m2'], 'reference': {'answer':'仅为协议测试'}},
                    'metadata': {'purpose':'protocol_test_only', 'not_product_evaluation':True}}
            path=Path(directory)/'cases.json';path.write_text(json.dumps([case]))
            cases,digest=load_cases(path)
            args=SimpleNamespace(phoenix_url=options.phoenix_url,ollama_url=f'http://127.0.0.1:{server.server_port}',model='protocol-test-model',judge_model='protocol-test-model',timeout=10,context_tokens=8192,
                                 dataset='v2-PROTOCOL-TEST-ONLY',name='PROTOCOL TEST ONLY - not model quality',repetitions=2,output=ROOT/'evals/output/protocol-test')
            result=run_experiment(args,cases,digest)
            assert result==0
            latest=max(args.output.glob('summary-*.json'),key=lambda p:p.stat().st_mtime)
            summary=json.loads(latest.read_text())
            assert all(b['scored']==2 and b['passed']==2 for b in summary['metrics'].values())
            # Verify round-trip from the actual server, not only local records.
            from phoenix.client import Client
            client=Client(base_url=options.phoenix_url)
            experiment=client.experiments.get_experiment(experiment_id=summary['experimentId'])
            assert len(experiment['task_runs'])==2, experiment
            records=[json.loads(line) for line in Path(summary['results']).read_text().splitlines()]
            trace_ids=[record['taskTraceId'] for record in records]
            for attempt in range(20):
                spans=client.spans.get_spans(project_identifier='jetaime-v2-experiments',trace_ids=trace_ids)
                if len(spans)>=8:break
                time.sleep(0.2)
            assert len(spans)==8, f"missing workflow spans: {len(spans)}"
            assert {s['name'] for s in spans} >= {'workflow.qa','build_context','model_call','validate_result'}
            eval_spans=client.spans.get_spans(project_identifier='jetaime-v2-evaluators')
            assert any(s['name']=='ollama.judge' for s in eval_spans), 'judge spans missing'
            assert len(experiment.get('evaluation_runs',{}))>=8, experiment.keys()
            print('Phoenix 数据集 / 重复运行 / CODE + LLM 评分 / Trace 协议验证通过；不代表模型效果。')
    finally:
        server.shutdown();server.server_close();thread.join()

if __name__=='__main__':main()

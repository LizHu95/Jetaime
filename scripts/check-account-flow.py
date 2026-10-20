#!/usr/bin/env python3
"""Run browser acceptance against a real API in a disposable PostgreSQL schema."""
import os
import sys
import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import subprocess
import tempfile
import time
import urllib.parse
import urllib.request
import uuid
from datetime import datetime, timedelta

from dev import ROOT, load_env


def main():
    env = os.environ.copy()
    load_env(ROOT / 'local/.env', env, set(env))
    url = env.get('JETAIME_TEST_DATABASE_URL') or env.get('JETAIME_DATABASE_URL')
    if not url:
        raise SystemExit('Configure JETAIME_TEST_DATABASE_URL or JETAIME_DATABASE_URL')
    schema = 'web_account_test_' + uuid.uuid4().hex
    parts = urllib.parse.urlsplit(url)
    if parts.scheme not in ('postgres', 'postgresql'):
        raise SystemExit('This checker requires a PostgreSQL URL')
    params = urllib.parse.parse_qs(parts.query)
    admin_env = {**env, 'PGDATABASE': urllib.parse.unquote(parts.path.lstrip('/')),
                 'PGHOST': params.get('host', [parts.hostname or 'localhost'])[0],
                 'PGPORT': str(parts.port or 5432)}
    if parts.username:
        admin_env['PGUSER'] = urllib.parse.unquote(parts.username)
    if parts.password:
        admin_env['PGPASSWORD'] = urllib.parse.unquote(parts.password)
    for key in ('user', 'password', 'port', 'sslmode'):
        if key in params:
            admin_env['PG' + key.upper()] = params[key][0]

    def sql(statement):
        result = subprocess.run(['psql', '-X', '-v', 'ON_ERROR_STOP=1', '-c', statement], env=admin_env, capture_output=True)
        if result.returncode:
            raise RuntimeError('PostgreSQL test schema operation failed')

    query = urllib.parse.parse_qs(parts.query)
    query['search_path'] = [schema]
    env['JETAIME_DATABASE_URL'] = parts.scheme + '://' + parts.netloc + parts.path + '?' + urllib.parse.urlencode(query, doseq=True)
    env.update(WEB_LIVE_CHAT='1', JETAIME_TEST_MODEL_KEY='local-test-key', WEB_LIVE_AUTH='1', WEB_PORT='15173', API_PROXY_TARGET='http://127.0.0.1:18081', CI='1')
    class FixtureModel(BaseHTTPRequestHandler):
        attempts = {}
        lock = threading.Lock()

        def log_message(self, *_):
            pass

        def do_POST(self):
            body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
            if self.path.rstrip('/').endswith('/embeddings'):
                self.send_json({'data': [{'index': 0, 'embedding': [1, 0, 0]}], 'usage': {'total_tokens': 1}})
                return
            system = body['messages'][0]['content']
            if '提取有来源的共享空间记忆' in system:
                self.send_completion({'candidates': []}, body['model'])
                return
            payload = json.loads(body['messages'][-1]['content'])
            prompt = payload.get('question') or payload['context']['messages'][-1]['text']
            source = payload['context']['messages'][-1]['id']
            with self.lock:
                count = self.attempts.get(prompt, 0)
                self.attempts[prompt] = count + 1
            time.sleep(0.6)
            if 'AI_FAIL_ONCE' in prompt and count == 0:
                self.send_response(503)
                self.end_headers()
                self.wfile.write(b'{"error":{"message":"fixture failure"}}')
                return
            if '主Agent' in system:
                output = {'name': 'extract_todos', 'arguments': {'question': prompt}} if '提取待办' in prompt else {'name': 'answer_question', 'arguments': {'question': prompt, 'context': 'general'}}
                draft = payload['context'].get('reminderDraft')
                if prompt == '提醒我吃饭' or (draft and prompt in ('明天', '中午12点')):
                    day = (datetime.fromisoformat(payload['context']['request']['sentAt']) + timedelta(days=1)).strftime('%Y-%m-%d')
                    output = {'name': 'set_reminder', 'arguments': {'action': 'save', 'title': '吃饭', 'date': draft.get('date', '') if draft else '', 'time': ''}}
                    if prompt == '明天':
                        output['arguments']['date'] = day
                    if prompt == '中午12点':
                        output['arguments']['time'] = '12:00'
            elif '独立核对候选行动' in system:
                output = {'reviews': [{'candidateIndex': i, 'status': 'active', 'evidenceMessageIds': candidate['sourceMessageIds'], 'reason': '直接本人安排，没有后续完成'} for i, candidate in enumerate(payload['candidates'])]}
            elif '提取明确' in system:
                evidence = next((m for m in payload['context']['messages'] if '我明天负责准备食材' in m['text']), None)
                output = {'suggestions': [] if evidence is None else [{'title': '准备食材', 'assigneeId': evidence['senderId'], 'timeExpression': '明天', 'sourceMessageIds': [evidence['id']]}]}
            else:
                output = {'status': 'answered', 'answer': '测试 AI 回答：' + prompt, 'sourceMessageIds': [source]}
            self.send_completion(output, body['model'])

        def send_completion(self, output, model):
            answer = json.dumps(output, ensure_ascii=False)
            self.send_json({'id': 'fixture', 'object': 'chat.completion', 'model': model,
                               'choices': [{'index': 0, 'message': {'role': 'assistant', 'content': answer}, 'finish_reason': 'stop'}],
                               'usage': {'prompt_tokens': 10, 'completion_tokens': 10, 'total_tokens': 20}})

        def send_json(self, output):
            data = json.dumps(output).encode()
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Content-Length', str(len(data)))
            self.end_headers()
            self.wfile.write(data)

    model_server = ThreadingHTTPServer(('127.0.0.1', 18082), FixtureModel)
    threading.Thread(target=model_server.serve_forever, daemon=True).start()
    sql('CREATE SCHEMA ' + schema)
    try:
        with tempfile.TemporaryDirectory(prefix='jetaime-account-check-') as folder:
            binary = str(Path(folder) / 'api')
            memory_config = Path(folder) / 'memory.json'
            memory_config.write_text(json.dumps({
                'embedding': {'base_url': 'http://127.0.0.1:18082/v1', 'model': 'test-embedding',
                              'revision': 'v1', 'api_key_env': 'JETAIME_TEST_MODEL_KEY',
                              'dimension': 3, 'max_input_bytes': 24576},
                'extraction': {'provider': 'openai-compatible', 'base_url': 'http://127.0.0.1:18082/v1',
                               'model': 'test-memory', 'api_key_env': 'JETAIME_TEST_MODEL_KEY',
                               'output_mode': 'json_object', 'context_tokens': 32768}
            }))
            subprocess.run(['go', 'build', '-o', binary, './cmd/api'], cwd=ROOT / 'services/api', env=env, check=True)
            subprocess.run([binary, '-migrate'], env=env, check=True)
            with open(Path(folder) / 'api.log', 'w') as log:
                api = subprocess.Popen([binary, '-auth', '-chat-ai', '-addr', '127.0.0.1:18081', '-provider', 'openai-compatible', '-model', 'test-shared-ai', '-model-url', 'http://127.0.0.1:18082/v1', '-api-key-env', 'JETAIME_TEST_MODEL_KEY', '-context-tokens', '16384', '-memory-config', str(memory_config)], env=env, stdout=log, stderr=log)
                try:
                    for _ in range(100):
                        if api.poll() is not None:
                            raise RuntimeError('Isolated API failed to start')
                        try:
                            with urllib.request.urlopen('http://127.0.0.1:18081/test/healthz', timeout=1):
                                break
                        except OSError:
                            time.sleep(0.1)
                    else:
                        raise RuntimeError('Isolated API startup timed out')
                    subprocess.run(['npm', 'run', 'test:e2e', '--', '--workers=2', *sys.argv[1:]], cwd=ROOT / 'apps/web', env=env, check=True)
                finally:
                    api.terminate()
                    try:
                        api.wait(timeout=5)
                    except subprocess.TimeoutExpired:
                        api.kill()
                        api.wait()
    finally:
        model_server.shutdown()
        model_server.server_close()
        sql('DROP SCHEMA ' + schema + ' CASCADE')
        print('Disposable account test schema removed')


if __name__ == '__main__':
    main()

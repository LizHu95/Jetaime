#!/usr/bin/env python3
"""Run frozen local replay cases as Phoenix experiments (no business DB access)."""
import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import uuid

from eval_replay import ROOT, build, model_environment, private_json


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def examples_for(suite, suite_hash, snapshot_hash):
    return [{'input': {'caseId': case['id'], 'question': case['question'],
                       'groupId': case['groupId'], 'cutoffSeq': case['cutoffSeq'], 'at': case['at']},
             'output': {**case.get('expected', {}), 'reviewed': case.get('reviewed', False)},
             'metadata': {'suiteSha256': suite_hash, 'snapshotSha256': snapshot_hash}}
            for case in suite['cases']]


def score_evaluator(name):
    def evaluate(output):
        value = output.get('scores', {}).get(name)
        return {'score': None if value is None else float(value),
                'label': 'not_scored' if value is None else ('pass' if value else 'fail')}
    evaluate.__name__ = name
    return evaluate


def metric_evaluator(name):
    def evaluate(output):
        value = output.get('metrics', {}).get(name)
        return {'score': value, 'label': 'unavailable' if value is None else str(value)}
    evaluate.__name__ = name
    return evaluate


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--source-root', type=Path, default=ROOT)
    parser.add_argument('--binary', type=Path, help='use an already built replay binary')
    parser.add_argument('--snapshot', type=Path, required=True)
    parser.add_argument('--cases', type=Path, required=True)
    parser.add_argument('--experiment', required=True)
    parser.add_argument('--endpoint', default='http://127.0.0.1:16006')
    parser.add_argument('--dataset', help='default includes suite and snapshot hashes')
    parser.add_argument('--model-config', type=Path)
    parser.add_argument('--env-file', type=Path, help='model credential environment file')
    parser.add_argument('--model-profile')
    parser.add_argument('--repetitions', type=int, default=3)
    parser.add_argument('--case-timeout', type=int, default=300)
    parser.add_argument('--output-dir', type=Path)
    parser.add_argument('--dry-run', action='store_true', help='validate locally; no Phoenix or model requests')
    parser.add_argument('--capture-content', action='store_true', help='include bounded prompt/output text in traces')
    args = parser.parse_args()
    if not 1 <= args.repetitions <= 20 or args.case_timeout <= 0:
        parser.error('repetitions must be 1-20, case-timeout must be positive')
    root = args.source_root.resolve()
    binary = args.binary.resolve() if args.binary else build(root)
    snapshot, cases = args.snapshot.resolve(), args.cases.resolve()
    hashes = {'snapshotSha256': digest(snapshot), 'suiteSha256': digest(cases)}
    suite = json.loads(cases.read_text())
    common = [str(binary), '-snapshot', str(snapshot), '-cases', str(cases)]
    # Preflight is deliberately before importing the SDK or creating any dataset.
    subprocess.run([*common, '-dry-run', '-output', '-'], check=True)
    if args.dry_run:
        return

    import httpx
    from phoenix.client import Client
    from phoenix.client.experiments import run_experiment
    from opentelemetry import trace
    from opentelemetry.propagate import inject
    from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter
    from opentelemetry.sdk.resources import Resource
    from opentelemetry.sdk.trace import TracerProvider
    from opentelemetry.sdk.trace.export import SimpleSpanProcessor

    source = json.loads(subprocess.check_output([str(binary), '-build-info'], text=True))
    endpoint = args.endpoint.rstrip('/')
    api_key = os.environ.get('PHOENIX_API_KEY')
    headers = {'Authorization': 'Bearer ' + api_key} if api_key else {}
    provider = TracerProvider(resource=Resource.create({
        'service.name': 'jetaime-eval', 'openinference.project.name': 'jetaime-eval',
        'deployment.branch': source['branch'], 'service.version': source['commit']}))
    provider.add_span_processor(SimpleSpanProcessor(OTLPSpanExporter(endpoint=endpoint + '/v1/traces', headers=headers)))
    trace.set_tracer_provider(provider)
    client = Client(base_url=endpoint, api_key=api_key)
    name = args.dataset or 'chat-replay-' + hashes['suiteSha256'][:12] + '-' + hashes['snapshotSha256'][:12]
    examples = examples_for(suite, hashes['suiteSha256'], hashes['snapshotSha256'])
    try:
        dataset = client.datasets.get_dataset(dataset=name)
    except (httpx.HTTPStatusError, ValueError) as error:
        missing = (isinstance(error, httpx.HTTPStatusError) and error.response.status_code == 404) or str(error) == f"Dataset not found: {name}"
        if not missing:
            raise
        dataset = client.datasets.create_dataset(name=name, examples=examples,
                    dataset_description='Frozen snapshot replay; fixed retrieval, simulated reminders and maps.')
    # Reusing an experiment dataset must NEVER quietly update its examples.
    canonical = lambda values: sorted(json.dumps({key: item[key] for key in ('input', 'output', 'metadata')},
                                                ensure_ascii=False, sort_keys=True) for item in values)
    if canonical(dataset.examples) != canonical(examples):
        raise ValueError('Existing Phoenix dataset differs; choose a new dataset name/version.')

    outdir = args.output_dir.resolve() if args.output_dir else root / 'outputs/evals/replay' / (
        datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%SZ-') + uuid.uuid4().hex[:8])
    outdir.mkdir(parents=True, exist_ok=False)
    config = args.model_config.resolve() if args.model_config else root / 'local/models.json'
    model_args = ['-model-config', str(config)]
    if args.model_profile:
        model_args += ['-model-profile', args.model_profile]
    metadata = {**hashes, 'source': source, 'binarySha256': digest(binary),
                'modelConfigSha256': digest(config), 'repetitions': args.repetitions,
                'datasetId': dataset.id, 'datasetVersionId': dataset.version_id,
                'retrieval': 'fixed_message_ids', 'maps': 'recorded_only', 'reminders': 'simulated'}
    private_json(outdir / 'metadata.json', metadata)
    env = model_environment(root, args.env_file.resolve() if args.env_file else None)
    env['JETAIME_PHOENIX_TRACES_ENDPOINT'] = endpoint + '/v1/traces'
    env['JETAIME_TRACE_CAPTURE_CONTENT'] = str(args.capture_content).lower()
    env['OTEL_RESOURCE_ATTRIBUTES'] = 'openinference.project.name=jetaime-eval'
    env['OTEL_SDK_DISABLED'] = 'false'
    # Both Go and Python exporters use the same explicitly selected endpoint/key.
    if api_key:
        env['PHOENIX_API_KEY'] = api_key

    def task(input):
        if digest(snapshot) != hashes['snapshotSha256'] or digest(cases) != hashes['suiteSha256']:
            raise ValueError('Frozen input changed during experiment')
        if digest(config) != metadata['modelConfigSha256'] or digest(binary) != metadata['binarySha256']:
            raise ValueError('Model config or binary changed during experiment')
        path = outdir / (uuid.uuid4().hex + '.json')
        carrier = {}
        inject(carrier)
        task_env = dict(env, JETAIME_EVAL_TRACEPARENT=carrier.get('traceparent', ''))
        completed = subprocess.run([*common, '-case-id', input['caseId'], '-output', str(path),
                                   '-trace', '-case-timeout', str(args.case_timeout) + 's', *model_args],
                                  cwd=root / 'services/api', env=task_env,
                                  capture_output=True, text=True, timeout=args.case_timeout + 30)
        if not path.exists():
            # Never forward subprocess stderr containing unexpected provider diagnostics.
            raise RuntimeError('Replay produced no report; subprocess exit=' + str(completed.returncode))
        report = json.loads(path.read_text())
        if len(report['results']) != 1:
            raise RuntimeError('Expected exactly one replay result')
        row = report['results'][0]
        # Full run/prompts stay in the local private report; the experiment table
        # gets the answer, scores, metrics and Trace link, not all source messages.
        return {key: row.get(key) for key in ('caseId', 'status', 'error', 'answer', 'metrics', 'scores',
                'traceId', 'simulatedReminderOperations', 'mapFixtureMisses')}

    evaluators = {name: score_evaluator(name) for name in (
        'execution_success', 'map_fixtures_complete', 'required_tools', 'forbidden_tools',
        'answer_terms', 'required_sources')}
    evaluators.update({name: metric_evaluator(name) for name in (
        'promptTokens', 'completionTokens', 'durationMs', 'firstInputByteEstimate',
        'cumulativeInputByteEstimate', 'searchCalls')})
    try:
        experiment = run_experiment(dataset=dataset, task=task, evaluators=evaluators,
            experiment_name=args.experiment, experiment_metadata=metadata,
            repetitions=args.repetitions, retries=0, timeout=args.case_timeout + 60, client=client)
        private_json(outdir / 'experiment.json', {'experimentId': experiment['experiment_id'],
                     'datasetId': dataset.id, 'datasetVersionId': dataset.version_id})
        print('Local replay reports: ' + str(outdir))
    finally:
        provider.shutdown()


if __name__ == '__main__':
    main()

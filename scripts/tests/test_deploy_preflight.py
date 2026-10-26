"""Exercise deployment failure paths with an isolated fake API binary."""
import hashlib
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


class DeployPreflightTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.api = self.root / 'installed' / 'api'
        self.api.mkdir(parents=True)
        self.env = dict(os.environ)
        for key in ('JETAIME_MEMORY_CONFIG', 'JETAIME_PHOENIX_TRACES_ENDPOINT'):
            self.env.pop(key, None)

    def package(self, directory, check_exit):
        directory.mkdir(parents=True, exist_ok=True)
        shutil.copy2(ROOT / 'deploy/api/start.sh', directory / 'start.sh')
        binary = '#!/bin/bash\ncase "$1" in\n-migrate) exit 0;;\n-memory-check) echo "pipeline incompatible"; exit ' + str(check_exit) + ';;\n*) exit 99;;\nesac\n'
        for arch in ('amd64', 'arm64'):
            path = directory / ('api-linux-' + arch)
            path.write_text(binary)
            path.chmod(0o755)
        for name in ('models.example.json', 'memory.example.json', 'README.md', '.env', 'models.json', 'memory.json'):
            (directory / name).write_text('' if name == '.env' else 'incoming')
        (directory / 'phoenix').mkdir()
        (directory / 'phoenix/install.sh').write_text('incoming')
        files = [p for p in directory.rglob('*') if p.is_file()]
        (directory / 'SHA256SUMS').write_text(''.join(
            f'{hashlib.sha256(p.read_bytes()).hexdigest()}  {p.relative_to(directory)}\n' for p in files
        ))

    def test_restart_rejects_mismatch_before_removing_pid(self):
        self.package(self.api, 1)
        (self.api / 'runtime').mkdir()
        pid = self.api / 'runtime/api.pid'
        pid.write_text('123456789\n')
        result = subprocess.run(['bash', str(self.api / 'start.sh'), 'restart'], env=self.env, capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('未停止 API', result.stderr)
        self.assertEqual(pid.read_text(), '123456789\n')

    def test_installer_checks_before_replacing_files(self):
        source = (ROOT / 'deploy/push.sh').read_text()
        installer = source.split("api_installer=$(cat <<'SH'\n", 1)[1].split('\nSH\n)', 1)[0]
        for check_exit in (1, 0):
            with self.subTest(check_exit=check_exit):
                staging = self.root / f'upload-{check_exit}'
                package = self.root / f'package-{check_exit}' / 'api'
                self.package(package, check_exit)
                staging.mkdir()
                with tarfile.open(staging / 'api-deploy.tar.gz', 'w:gz') as archive:
                    archive.add(package, arcname='api')
                (self.api / 'memory.json').write_text('original')
                (self.api / 'api-linux-amd64').write_text('original-binary')
                result = subprocess.run(['bash', '-c', installer, '--', str(staging), str(self.api.parent)], env=self.env, capture_output=True, text=True)
                if check_exit:
                    self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                    self.assertEqual((self.api / 'memory.json').read_text(), 'original')
                    self.assertEqual((self.api / 'api-linux-amd64').read_text(), 'original-binary')
                else:
                    self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                    self.assertEqual((self.api / 'memory.json').read_text(), 'incoming')


if __name__ == '__main__':
    unittest.main()

"""Run on the audited host after acceptance. Read secrets from stdin, never argv."""
import json
import os
from pathlib import Path
import pwd
import re
import secrets
import shutil
import subprocess
import sys

root = Path('/opt/atoms_lab')
release = Path(sys.argv[1]).resolve()
if release.parent != root / 'releases' or not re.fullmatch(r'[a-zA-Z0-9._-]+', release.name):
    raise SystemExit('Unexpected release path')
for relative in ['atoms', 'web/dist/index.html', 'runner/server.cjs']:
    if not (release / relative).is_file():
        raise SystemExit('Incomplete release')
config = json.load(sys.stdin)
api_key = config['api_key']
if not api_key or any(c in api_key for c in '\r\n\x00'):
    raise SystemExit('Invalid credential')
image_id = json.loads(subprocess.check_output(['docker', 'image', 'inspect', 'atoms-runner:local']))[0]['Id']
runtime = json.loads(subprocess.check_output(['docker', 'info', '--format', '{{json .Runtimes}}']))
if 'runsc' not in runtime:
    raise SystemExit('runsc unavailable')
try:
    owner = pwd.getpwnam('atoms-lab')
except KeyError:
    subprocess.run(['useradd', '--system', '--no-create-home', '--shell', '/usr/sbin/nologin', 'atoms-lab'], check=True)
    owner = pwd.getpwnam('atoms-lab')
for name in ['data', 'runner-data']:
    (root / name).mkdir(mode=0o700, exist_ok=True)
os.chown(root / 'data', owner.pw_uid, owner.pw_gid)
env_dir = Path('/etc/atoms-lab')
env_dir.mkdir(mode=0o700, exist_ok=True)
runner_token = secrets.token_hex(32)
app = {
    'ATOMS_ADDR': '172.18.0.1:8090', 'ATOMS_DATA_DIR': str(root / 'data'),
    'ATOMS_WEB_DIR': str(root / 'current/web/dist'), 'ATOMS_PUBLIC_URL': 'https://atoms.linhengyi.xyz', 'ATOMS_TRUST_PROXY': '1',
    'ATOMS_LLM_BASE_URL': 'https://api.linhengyi.xyz/v1', 'ATOMS_LLM_MODEL': 'chat',
    'ATOMS_LLM_PROTOCOL': 'chat_completions', 'ATOMS_LLM_TOOL_MODE': 'json',
    'ATOMS_LLM_CONFIG_REVISION': 'gateway-chat-json-v3', 'ATOMS_LLM_API_KEY': api_key,
    'ATOMS_MAX_OUTPUT_TOKENS': '81920', 'ATOMS_LLM_TPM': '1000000',
    'ATOMS_RUN_SECONDS': '1200',
    'ATOMS_MAX_CONCURRENT': '1', 'ATOMS_DAILY_CALLS': '2000', 'ATOMS_WORKSPACE_DAILY_CALLS': '1000',
    'ATOMS_RUNNER_URL': 'http://127.0.0.1:8091', 'ATOMS_RUNNER_TOKEN': runner_token,
}
runner = {
    'ATOMS_RUNNER_TOKEN': runner_token, 'ATOMS_RUNNER_HOST': '127.0.0.1', 'ATOMS_RUNNER_PORT': '8091',
    'ATOMS_RUNNER_DATA': str(root / 'runner-data'), 'ATOMS_SANDBOX_RUNTIME': 'runsc',
    'ATOMS_RUNNER_IMAGE': image_id, 'ATOMS_SANDBOX_MEMORY': '768m', 'ATOMS_SANDBOX_CPUS': '0.5',
}
for name, values in [('app.env', app), ('runner.env', runner)]:
    target = env_dir / name
    if target.exists():
        shutil.copy2(target, env_dir / (name + '.previous'))
    with os.fdopen(os.open(target, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600), 'w') as f:
        for k, v in values.items():
            f.write(k + '="' + v.replace('\\', '\\\\').replace('"', '\\"') + '"\n')
        f.flush()
        os.fsync(f.fileno())
for service in ['atoms-web.service', 'atoms-runner.service']:
    shutil.copyfile(release / 'deploy' / service, Path('/etc/systemd/system') / service)
current = root / 'current'
previous = str(current.resolve()) if current.exists() else None
link = root / ('current-' + secrets.token_hex(4))
link.symlink_to(release, target_is_directory=True)
os.replace(link, current)
subprocess.run(['systemctl', 'daemon-reload'], check=True)
subprocess.run(['systemctl', 'enable', 'atoms-runner', 'atoms-web'], check=True)
subprocess.run(['systemctl', 'restart', 'atoms-runner', 'atoms-web'], check=True)
receipt = {'release': str(release), 'previous': previous, 'domain': 'atoms.linhengyi.xyz', 'runtime': 'runsc', 'runner_image': image_id}
(root / 'release-receipt.json').write_text(json.dumps(receipt, indent=2))
print(json.dumps(receipt))

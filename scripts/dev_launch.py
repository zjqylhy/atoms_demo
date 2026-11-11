"""Local development launcher. Secrets live only in child process environments."""
import getpass
import os
from pathlib import Path
import secrets
import subprocess
import time
import json
import urllib.request

root = Path(__file__).resolve().parents[1]
env = os.environ.copy()
secret = env.get('ATOMS_LLM_API_KEY') or getpass.getpass('Gateway API key (hidden): ')
# Check credentials before starting workers; never print response bodies or the key.
request = urllib.request.Request('https://gateway.example.com/v1/models',
                                 headers={'Authorization': 'Bearer ' + secret})
try:
    with urllib.request.urlopen(request, timeout=20) as response:
        models = json.load(response)
    if not any(item.get('id') == 'chat' for item in models.get('data', [])):
        raise ValueError('chat model unavailable')
except Exception:
    raise SystemExit('Gateway credential validation failed; no workers started.')
print('Gateway credential verified; chat model is available.', flush=True)
env.update(ATOMS_LLM_API_KEY=secret, ATOMS_LLM_BASE_URL='https://gateway.example.com/v1',
           ATOMS_LLM_MODEL='chat', ATOMS_LLM_TOOL_MODE='json', ATOMS_LLM_PROTOCOL='chat_completions',
           ATOMS_MAX_OUTPUT_TOKENS='8192',
           ATOMS_LLM_CONFIG_REVISION='gateway-chat-json-dev-v2', ATOMS_ADDR='127.0.0.1:8090',
           ATOMS_RUNNER_URL='http://127.0.0.1:8091', ATOMS_RUNNER_TOKEN=secrets.token_hex(32),
           ATOMS_RUNNER_LOCAL_DEV='1', ATOMS_SANDBOX_RUNTIME='runc')
secret = ''
children = []
try:
    children.append(subprocess.Popen([str(root / 'bin' / 'atoms.exe')], cwd=root, env=env))
    print('App started; waiting for the local execution image.', flush=True)
    while subprocess.run(['docker', 'image', 'inspect', 'atoms-runner:local'], stdout=subprocess.DEVNULL,
                         stderr=subprocess.DEVNULL, timeout=15).returncode:
        if children[0].poll() is not None:
            raise RuntimeError('App exited')
        time.sleep(3)
    runner_env = {k: v for k, v in env.items() if not k.startswith('ATOMS_LLM_')}
    children.append(subprocess.Popen(['node', 'runner/server.cjs'], cwd=root, env=runner_env))
    runner_stamp = (root / 'runner' / 'server.cjs').stat().st_mtime_ns
    while all(child.poll() is None for child in children):
        candidate = root / 'bin' / 'atoms-next.exe'
        if candidate.exists():
            children[0].terminate()
            children[0].wait(timeout=15)
            os.replace(candidate, root / 'bin' / 'atoms.exe')
            children[0] = subprocess.Popen([str(root / 'bin' / 'atoms.exe')], cwd=root, env=env)
            print('App restarted with rebuilt binary.', flush=True)
        stamp = (root / 'runner' / 'server.cjs').stat().st_mtime_ns
        if stamp != runner_stamp:
            children[1].terminate()
            children[1].wait(timeout=15)
            children[1] = subprocess.Popen(['node', 'runner/server.cjs'], cwd=root, env=runner_env)
            runner_stamp = stamp
        time.sleep(1)
except KeyboardInterrupt:
    pass
finally:
    for child in children:
        if child.poll() is None:
            child.terminate()
    for child in children:
        child.wait(timeout=15)

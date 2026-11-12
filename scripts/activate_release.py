"""Activate an uploaded, verified release. Credential is sent only on SSH stdin."""
import argparse
import getpass
import json
from pathlib import Path
import re
import subprocess

parser = argparse.ArgumentParser()
parser.add_argument('--ssh-key', required=True)
parser.add_argument('--host', default='47.113.231.252')
parser.add_argument('--release', required=True)
args = parser.parse_args()
if not re.fullmatch(r'[0-9]{8}T[0-9]{6}Z', args.release):
    raise SystemExit('Invalid release identifier')
credential = getpass.getpass('Gateway API key for server environment (hidden): ')
target = '/opt/atoms_lab/releases/' + args.release
command = ['ssh', '-i', str(Path(args.ssh_key).resolve()), '-o', 'IdentitiesOnly=yes',
           '-o', 'BatchMode=yes', '-o', 'StrictHostKeyChecking=yes', '-o', 'ConnectTimeout=12',
           'root@' + args.host, 'python3 ' + target + '/deploy/install.py ' + target]
result = subprocess.run(command, input=json.dumps({'api_key': credential}), text=True, encoding='utf-8', errors='replace',
                        stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=90)
credential = ''
print(result.stdout)
if result.returncode:
    # Deployment subprocesses never receive credentials in argv or print input.
    print(result.stderr)
    raise SystemExit(result.returncode)

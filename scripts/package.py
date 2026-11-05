#!/usr/bin/env python3
"""Build deterministic release archives from a clean, committed public checkout."""
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tarfile
import tempfile
import time
import zipfile

root = Path(__file__).resolve().parent.parent
os.chdir(root)
version = sys.argv[1] if len(sys.argv) == 2 else ''
if not re.fullmatch(r'\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?', version):
    sys.exit('Usage: python3 scripts/package.py VERSION')
epoch = int(os.environ.get('SOURCE_DATE_EPOCH') or subprocess.check_output(['git', 'show', '-s', '--format=%ct', 'HEAD'], text=True).strip())
expected = 'go' + re.search(r'^go (\S+)', Path('go.mod').read_text(), re.M)[1]
actual = subprocess.check_output(['go', 'env', 'GOVERSION'], text=True).strip()
if actual != expected:
    sys.exit(f'Use the pinned toolchain {expected}; found {actual}')
dist = root / 'dist'
dist.mkdir(exist_ok=True)
artifacts = []
platforms = os.environ.get('HPO_BUILD_PLATFORMS', 'darwin-amd64 darwin-arm64 linux-amd64 linux-arm64 windows-amd64 windows-arm64').split()
for platform in platforms:
    if platform not in ('darwin-amd64','darwin-arm64','linux-amd64','linux-arm64','windows-amd64','windows-arm64'):
        sys.exit('Unsupported platform: ' + platform)
    system, arch = platform.split('-')
    env = {**os.environ, 'CGO_ENABLED':'0', 'GOOS':system, 'GOARCH':arch, 'GOTOOLCHAIN':'local', 'GOFLAGS':''}
    with tempfile.TemporaryDirectory() as temp:
        binary = Path(temp) / 'hpo'
        subprocess.run(['go','build','-trimpath','-buildvcs=false','-mod=readonly','-ldflags',f'-s -w -X github.com/hyperoptimizer/cli/internal/cli.Version={version}','-o',str(binary),'./cmd/hpo'],env=env,check=True)
        suffix = '.exe' if system == 'windows' else ''
        contents = {f'hpo{suffix}':binary.read_bytes(),f'hyperoptimizer{suffix}':binary.read_bytes(), 'README.md':Path('README.md').read_bytes(), 'LICENSE':Path('LICENSE').read_bytes()}
        name = f'hpo_{version}_{system}_{arch}'
        path = dist / (name + ('.zip' if suffix else '.tar.gz'))
        if suffix:
            with zipfile.ZipFile(path,'w',compression=zipfile.ZIP_STORED) as archive:
                for name,data in sorted(contents.items()):
                    entry=zipfile.ZipInfo(name,time.gmtime(max(epoch,315532800))[:6])
                    entry.create_system=3
                    entry.external_attr=(0o100755 if name.endswith('.exe') else 0o100644)<<16
                    archive.writestr(entry,data)
        else:
            with path.open('wb') as output, gzip.GzipFile(filename='',mode='wb',fileobj=output,mtime=0) as compressed, tarfile.open(fileobj=compressed,mode='w',format=tarfile.USTAR_FORMAT) as archive:
                for name,data in sorted(contents.items()):
                    entry=tarfile.TarInfo(name);entry.size=len(data);entry.mtime=epoch;entry.mode=0o755 if name in ('hpo','hyperoptimizer') else 0o644
                    archive.addfile(entry,io.BytesIO(data))
        artifacts.append(path)
# Machine-readable Go module inventory, including tools and test dependencies.
raw=subprocess.check_output(['go','list','-mod=readonly','-m','-json','all'],text=True)
modules=[];decoder=json.JSONDecoder()
while raw.strip():
    item,end=decoder.raw_decode(raw.lstrip());raw=raw.lstrip()[end:]
    modules.append({k:item[k] for k in ('Path','Version','Sum','GoModSum') if k in item})
inventory=dist/'dependencies.json';inventory.write_text(json.dumps({'format':'go-module-inventory-v1','modules':sorted(modules,key=lambda x:x['Path'])},indent=2)+'\n');artifacts.append(inventory)
(dist/'checksums.txt').write_text(''.join(f'{hashlib.sha256(p.read_bytes()).hexdigest()}  {p.name}\n' for p in sorted(artifacts)))
print('Built',len(platforms),'platforms in',dist)

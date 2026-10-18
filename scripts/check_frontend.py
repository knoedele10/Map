"""Check inline JavaScript and local static resource references without a browser."""
import json
import re
import subprocess
from pathlib import Path

root = Path(__file__).resolve().parents[1]
web = root/'frontend'
out = root/'data/runtime/js-check'
out.mkdir(parents=True,exist_ok=True)
failures = []
count = 0
for page in [web/'index.html',web/'shuji/index.html']:
    text = page.read_text(encoding='utf-8')
    for i, match in enumerate(re.finditer(r'<script([^>]*)>(.*?)</script>', text, re.S)):
        if 'src=' in match[1] or 'importmap' in match[1]: continue
        path = out/(page.parent.name+str(i)+'.js'); path.write_text(match[2],encoding='utf-8')
        result = subprocess.run(['node','--check',str(path)],capture_output=True,text=True)
        count += 1
        if result.returncode: failures.append(result.stderr)
    for ref in re.findall(r'(?:src|href)="([^"<>]+)"',text):
        if ref.startswith(('http','data:','#')) or '${' in ref: continue
        ref = ref.split('?')[0]
        if ref.startswith('/static/'): target = web/ref[8:]
        elif ref.startswith('/'): continue
        else: target = page.parent/ref
        if not target.exists(): failures.append('missing '+str(target))
for source in [web/'integration.js',web/'shuji/connected.js',web/'shuji/longbao-games.js',web/'shuji/relic-viewer-3d.js']:
    result = subprocess.run(['node','--check',str(source)],capture_output=True,text=True)
    count += 1
    if result.returncode: failures.append(result.stderr)
print(json.dumps({'javascript_units':count,'failures':failures},ensure_ascii=False,indent=2))
raise SystemExit(bool(failures))

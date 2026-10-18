"""Exercise the running local service with isolated, disposable corpus files."""
import json
import time
import uuid
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
BASE = 'http://127.0.0.1:8090'
report = []

def request(path, body=None, method=None, headers=None):
    h = dict(headers or {})
    if isinstance(body, dict):
        body = json.dumps(body, ensure_ascii=False).encode(); h['Content-Type'] = 'application/json'
    req = urllib.request.Request(BASE + path, body, headers=h, method=method)
    with urllib.request.urlopen(req, timeout=180) as res:
        data = json.load(res)
    assert data['code'] == 0, data
    return data['data']

def wait_job(job_id):
    deadline = time.monotonic() + 180
    while time.monotonic() < deadline:
        job = request('/api/v1/ingest/jobs/' + job_id)
        if job['status'] != 'running':
            assert job['status'] == 'succeeded', job
            assert not job['stats']['files_failed'], job
            return job
        time.sleep(.4)
    raise TimeoutError(job_id)

def upload(source, text):
    boundary = 'shuji-' + uuid.uuid4().hex
    pieces = []
    for name, value in {'source': source, 'ingest': 'true', 'meta_topic': '联调验证'}.items():
        pieces.append(f'--{boundary}\r\nContent-Disposition: form-data; name="{name}"\r\n\r\n{value}\r\n'.encode())
    pieces.append(f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="probe.txt"\r\nContent-Type: text/plain\r\n\r\n{text}\r\n--{boundary}--\r\n'.encode())
    data = request('/api/v1/documents/upload', b''.join(pieces), 'POST', {'Content-Type':'multipart/form-data; boundary=' + boundary})
    assert data['job_id'], data
    wait_job(data['job_id'])
    return data['doc_id']

def main():
    health = request('/api/v1/health'); assert health['embedding']['ok'], health
    report.append({'check':'health','result':'pass'})
    for path in ['/', '/static/shuji/', '/static/shuji/relic-viewer-3d.js', '/static/shuji/assets/models/bronze-tree.glb', '/openapi.yaml']:
        with urllib.request.urlopen(BASE + path) as response: assert response.status == 200
    req = urllib.request.Request(BASE + '/chengdu-city.pmtiles', headers={'Range':'bytes=0-15'})
    with urllib.request.urlopen(req) as response:
        assert response.status == 206 and len(response.read()) == 16
    report.append({'check':'pages-assets-map-range','result':'pass'})
    result = request('/api/v1/search', {'query':'都江堰 鱼嘴 飞沙堰 宝瓶口', 'top_k':50, 'filters':{'abilities':['shuili']}})
    assert any('都江堰.md' == hit['chunk']['source'] for hit in result['hits']), result
    report.append({'check':'real-embedding-search-filter','result':'pass'})
    before = request('/api/v1/stats')['docs']
    folder = ROOT / 'data/corpus' / ('__integration_probe_' + uuid.uuid4().hex)
    assert folder.resolve().is_relative_to((ROOT/'data/corpus').resolve())
    ids = []
    try:
        text = '联调探针独立资料。' * 90
        for name in ['one.txt','two.txt']:
            ids.append(upload(folder.name + '/' + name,text))
        assert request('/api/v1/stats')['docs'] == before + 2
        updated_id = upload(folder.name + '/one.txt',text + '新增部分已成功更新。')
        assert updated_id == ids[0]
        result = request('/api/v1/search',{'query':'联调探针独立资料','top_k':50,'filters':{'topic':['联调验证']}})
        assert {hit['chunk']['doc_id'] for hit in result['hits']} == set(ids),result
        deleted = request('/api/v1/documents/' + ids[0], method='DELETE'); assert deleted['deleted']
        ids.pop(0)
        result = request('/api/v1/search',{'query':'联调探针独立资料','top_k':50,'filters':{'topic':['联调验证']}})
        assert {hit['chunk']['doc_id'] for hit in result['hits']} == set(ids),result
        report.append({'check':'upload-update-duplicate-delete-preserves-other-documents','result':'pass'})
    finally:
        for doc_id in ids: request('/api/v1/documents/' + doc_id, method='DELETE')
        if folder.exists():
            for file in folder.iterdir(): file.unlink()
            folder.rmdir()
    assert request('/api/v1/stats')['docs'] == before
    print(json.dumps(report,ensure_ascii=False,indent=2))

try:
    main()
except Exception as error:
    report.append({'check':'failure','result':str(error)}); raise
finally:
    (ROOT/'data/runtime/smoke-report.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf-8')

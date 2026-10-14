"""使用构建后的正式服务和 gallery-dl，离线验证轮播复用及 429 失败状态。"""
import json
import os
from pathlib import Path
import subprocess
import time
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
RUN = ROOT / ".test-runtime" / "instagram" / str(time.time_ns())
IMAGE = os.environ.get("SMOKE_IMAGE", "media-harbor:local")
PORT = int(os.environ.get("SMOKE_PORT", "18883"))
BASE = f"http://127.0.0.1:{PORT}/api"
NAME = "media-harbor-instagram-smoke-" + RUN.name

# 外部平台替换为容器内部 HTTP 素材，计数单独保存，不读取本地真实下载数据。
SOURCE = r'''
import json,threading
from collections import Counter
from http.server import BaseHTTPRequestHandler,ThreadingHTTPServer
counts=Counter()
lock=threading.Lock()
class Source(BaseHTTPRequestHandler):
    def do_GET(self):
        with lock:
            counts[self.path]+=1
            with open('/data/requests.json','w') as f: json.dump(counts,f)
        if self.path.startswith('/metadata/'):
            self.send_response(200); self.end_headers()
            self.wfile.write(json.dumps([1,2]).encode()); return
        if self.headers.get('X-Media-Fixture')!='preserved':
            self.send_response(400); self.end_headers(); return
        if '/limited/' in self.path:
            self.send_response(429); self.end_headers(); return
        body=b'GIF89a'+b'x'*1024
        self.send_response(200)
        self.send_header('Content-Type','image/gif')
        self.send_header('Content-Length',str(len(body)))
        self.end_headers(); self.wfile.write(body)
    def log_message(self,*args): pass
ThreadingHTTPServer(('127.0.0.1',8001),Source).serve_forever()
'''

# 两个入口共享同一个素材提取器；正式 Go 服务、缓存逻辑和下载器保持实际执行。
WRAPPER = r'''#!/opt/engines/bin/python3
import json,re,sys,urllib.request
from gallery_dl import extractor
from gallery_dl.extractor.common import Extractor,Message
class Fixture(Extractor):
    category='instagram'
    subcategory='fixture'
    directory_fmt=()
    filename_fmt='{post_id}_{num}.{extension}'
    def items(self):
        code=self.url.rstrip('/').split('/')[-1]
        if code=='avatar':
            code='avatar'
            nums=[1]
        else:
            nums=self.request_json('http://127.0.0.1:8001/metadata/'+code)
        for num in nums:
            data={'post_id':code,'num':num,'extension':'gif','post_url':'https://www.instagram.com/p/'+code+'/',
                  'owner_id':code,'username':code,'description':'Offline '+code,
                  '_http_headers':{'X-Media-Fixture':'preserved'}}
            yield Message.Directory,'',data
            yield Message.Url,'http://127.0.0.1:8001/media/'+code+'/'+str(num)+'.gif',data
extractor.find=lambda url: Fixture(re.match('.*',url))
if sys.argv[1:3]==['-u','-c']:
    code=sys.argv[3]; sys.argv=['gallery-dl']+sys.argv[4:]; exec(code)
else:
    import gallery_dl
    sys.exit(gallery_dl.main())
'''


def request(path, method="GET", data=None):
    body = json.dumps(data).encode() if data is not None else None
    req = urllib.request.Request(BASE + path, body, {"Content-Type": "application/json"}, method=method)
    with urllib.request.urlopen(req, timeout=10) as response:
        return json.load(response)


def wait(read, check):
    deadline = time.monotonic() + 30
    last = None
    while time.monotonic() < deadline:
        try:
            last = read()
            if check(last):
                return last
        except (OSError, ValueError):
            pass
        time.sleep(.2)
    raise AssertionError(f"离线容器验证超时: {last}")


def run():
    for name in ("data", "downloads", "logs", "archive", "fixture/bin"):
        (RUN / name).mkdir(parents=True)
    (RUN / "fixture/source.py").write_text(SOURCE)
    for name in ("python3", "gallery-dl"):
        path = RUN / "fixture/bin" / name
        path.write_text(WRAPPER)
        path.chmod(0o755)
    # 限流素材只验证退出状态和请求数，不实际等待引擎默认的一分钟。
    (RUN / "data/gallery-dl.json").write_text(json.dumps({"downloader": {"http": {"retries": 0, "sleep-429": 0}}}))
    args = ["docker", "run", "-d", "--rm", "--name", NAME, "--user", f"{os.getuid()}:{os.getgid()}",
            "-e", "INSTAGRAM_REQUEST_INTERVAL_SECONDS=0.02", "-e", "INSTAGRAM_429_COOLDOWN_SECONDS=0.04",
            "-p", f"127.0.0.1:{PORT}:8080", "-e", "PATH=/fixture/bin:/opt/engines/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"]
    for name in ("data", "downloads", "logs", "archive", "fixture"):
        args += ["-v", f"{RUN / name}:/{name}"]
    args += ["--entrypoint", "/bin/sh", IMAGE, "-c", "/opt/engines/bin/python3 /fixture/source.py & exec /app/server"]
    subprocess.check_output(args)
    try:
        settings = wait(lambda: request("/settings"), bool)
        settings["debug"]["test_item_limit"] = 10
        settings["instagram_concurrent"] = 2
        request("/settings", "PUT", settings)
        for code, expected in (("ready", "completed"), ("limited", "failed")):
            sub = request("/subscriptions", "POST", {"platform": "instagram", "url": f"https://www.instagram.com/{code}/", "enabled": False})
            # 创建解析与头像请求单独计数；只对比手动扫描后的帖子请求增量。
            before = json.loads((RUN / "data/requests.json").read_text()).get("/metadata/"+code, 0)
            request(f"/subscriptions/{sub['id']}/scan?kind=full", "POST")
            tasks = wait(lambda: request("/tasks"), lambda ts: len([t for t in ts if t["subscription_id"]==sub["id"] and t["kind"]=="download" and t["status"]==expected])==2)
            counts = json.loads((RUN / "data/requests.json").read_text())
            assert counts.get("/metadata/"+code, 0)-before == 1, counts
            assert counts.get(f"/media/{code}/1.gif")==1 and counts.get(f"/media/{code}/2.gif")==1, counts
            log = request(f"/subscriptions/{sub['id']}/log")["log"]
            assert "复用 Instagram 已解析媒体地址" in log and "媒体 HTTP 响应" in log, log
            if expected == "failed":
                assert "状态 429" in log and "下载引擎已返回；成功 false" in log, log
            print(json.dumps({"case": code, "download_status": expected, "post_requests_after_scan": 1, "media_requests": 2}, ensure_ascii=False), flush=True)
        print("Instagram 容器离线验收通过；素材与日志保存在 " + str(RUN), flush=True)
    except Exception:
        print(subprocess.check_output(["docker", "logs", NAME], text=True))
        raise
    finally:
        subprocess.run(["docker", "rm", "-f", NAME], stdout=subprocess.DEVNULL, check=False)


if __name__ == "__main__":
    run()

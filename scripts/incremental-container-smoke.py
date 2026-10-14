"""正式容器与真实 gallery-dl/yt-dlp，验证增量停止历史请求、补全越过缓存。"""
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import time

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("fixture", ROOT / "scripts/instagram-container-smoke.py")
fixture = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fixture)
RUN = ROOT / ".test-runtime/incremental" / str(time.time_ns())
NAME = "media-harbor-incremental-" + RUN.name
fixture.BASE = "http://127.0.0.1:18883/api"

# 分页、帖子详情和媒体各自计数，验证停止的是请求而不只是最终下载去重。
SOURCE = r'''
import json,threading
from collections import Counter
from http.server import BaseHTTPRequestHandler,ThreadingHTTPServer
from pathlib import Path
counts=Counter(); lock=threading.Lock()
class Source(BaseHTTPRequestHandler):
    def do_GET(self):
        with lock:
            counts[self.path]+=1
            Path('/data/requests.json').write_text(json.dumps(counts))
        if self.path.startswith('/profile/'):
            body=Path('/data/fixture-items.json').read_bytes()
        elif self.path.startswith('/post/') or self.path.startswith('/visit/'):
            body=b'{}'
        elif self.path.endswith('.mp4'):
            body=Path('/fixture/media.mp4').read_bytes()
        elif self.path.endswith('.png'):
            body=Path('/fixture/media.png').read_bytes()
        else:
            body=b'GIF89a'+b'x'*1024
        self.send_response(200);self.send_header('Content-Length',str(len(body)))
        self.end_headers();self.wfile.write(body)
    def log_message(self,*args): pass
ThreadingHTTPServer(('127.0.0.1',8001),Source).serve_forever()
'''

INSTAGRAM = r'''#!/opt/engines/bin/python3
import json,re,sys,urllib.request
from gallery_dl import extractor
from gallery_dl.extractor.common import Extractor,Message
class Fixture(Extractor):
    category='instagram'; subcategory='fixture'; directory_fmt=()
    filename_fmt='{post_id}_{num}.{extension}'
    def items(self):
        if self.url.rstrip('/').endswith('/avatar'):
            posts=['avatar']
        else:
            posts=self.request_json('http://127.0.0.1:8001/profile/instagram')
        for code in posts:
            self.request_json('http://127.0.0.1:8001/post/instagram/'+code)
            for num in [1,2] if code!='avatar' else [1]:
                data={'post_id':code,'num':num,'extension':'png','owner_id':'author',
                      'username':'author','description':code,'post_url':'https://www.instagram.com/p/'+code+'/'}
                yield Message.Directory,'',data
                yield Message.Url,'http://127.0.0.1:8001/media/instagram/'+code+'/'+str(num)+'.png',data
extractor.find=lambda url:Fixture(re.match('.*',url))
if '-c' in sys.argv:
    index=sys.argv.index('-c');code=sys.argv[index+1];sys.argv=['gallery-dl']+sys.argv[index+2:];exec(code)
else:
    import gallery_dl
    sys.exit(gallery_dl.main())
'''

# 保留 yt-dlp 的列表迭代、archive 判断和真实下载器，仅替换平台素材提取器。
YOUTUBE = r'''#!/opt/engines/bin/python3
import json,sys,urllib.request
import yt_dlp
from yt_dlp.extractor.common import InfoExtractor
class Profile(InfoExtractor):
    @classmethod
    def ie_key(cls): return 'YoutubeTab'
    _VALID_URL=r'https://www\.youtube\.com/@(?P<id>[^/?]+)'
    def _real_extract(self,url):
        posts=self._download_json('http://127.0.0.1:8001/profile/youtube', 'author')
        def entries():
            for code in posts:
                self._download_json('http://127.0.0.1:8001/visit/youtube/'+code, code)
                yield {'_type':'url','url':'https://www.youtube.com/watch?v='+code,'ie_key':'Youtube',
                       'id':code,'title':code,'channel_id':'author','channel':'author'}
        return self.playlist_result(entries(),'author','author',channel='author',channel_id='author')
class Video(InfoExtractor):
    _VALID_URL=r'https://www\.youtube\.com/watch\?v=(?P<id>[^&]+)'
    @classmethod
    def ie_key(cls): return 'Youtube'
    def _real_extract(self,url):
        code=self._match_id(url)
        self._download_json('http://127.0.0.1:8001/post/youtube/'+code, code)
        return {'id':code,'title':code,'channel':'author','channel_id':'author',
                'upload_date':'20261014','availability':'public',
                'formats':[{'format_id':'fixture','url':'http://127.0.0.1:8001/media/youtube/'+code+'.mp4',
                            'ext':'mp4','vcodec':'h264','acodec':'aac'}]}
original=yt_dlp.YoutubeDL.add_default_info_extractors
def install(self):
    original(self)
    self.add_info_extractor(Profile())
    self.add_info_extractor(Video())
yt_dlp.YoutubeDL.add_default_info_extractors=install
yt_dlp.main(sys.argv[1:])
'''


YOUTUBE_SDK = YOUTUBE.split("\n", 1)[1].replace("yt_dlp.main(sys.argv[1:])", "")
INSTAGRAM = INSTAGRAM.replace("exec(code)", "exec(" + repr(YOUTUBE_SDK) + ");exec(code)")


def counts():
    return json.loads((RUN / "data/requests.json").read_text())


def checked(platform, before):
    prefix = "/post/instagram/" if platform == "instagram" else "/visit/youtube/"
    return {key.removeprefix(prefix): value-before.get(key, 0) for key, value in counts().items()
            if key.startswith(prefix) and value > before.get(key, 0)}


def scan(sub, kind):
    task = fixture.request(f"/subscriptions/{sub['id']}/scan?kind={kind}", "POST")
    fixture.wait(lambda: fixture.request("/tasks"),
                 lambda tasks: next(t for t in tasks if t["id"] == task["id"])["status"] in ("completed", "failed"))
    tasks = fixture.wait(lambda: fixture.request("/tasks"),
                         lambda tasks: not any(t["subscription_id"] == sub["id"] and t["status"] in ("queued", "running") for t in tasks))
    assert all(t["status"] == "completed" for t in tasks if t["subscription_id"] == sub["id"]), tasks


def run():
    for name in ("data", "downloads", "logs", "archive", "fixture/bin"):
        (RUN / name).mkdir(parents=True)
    (RUN / "fixture/source.py").write_text(SOURCE)
    (RUN / "data/fixture-items.json").write_text('["cached"]')
    for name, script in (("python3", INSTAGRAM), ("gallery-dl", INSTAGRAM), ("yt-dlp", YOUTUBE)):
        path = RUN / "fixture/bin" / name
        path.write_text(script); path.chmod(0o755)
    args = ["docker", "run", "-d", "--rm", "--name", NAME, "--user", f"{os.getuid()}:{os.getgid()}",
            "-e", "INSTAGRAM_REQUEST_INTERVAL_SECONDS=0.02", "-e", "INSTAGRAM_429_COOLDOWN_SECONDS=0.04",
            "-p", "127.0.0.1:18883:8080", "-e", "PATH=/fixture/bin:/opt/engines/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"]
    for name in ("data", "downloads", "logs", "archive", "fixture"):
        args += ["-v", f"{RUN / name}:/{name}"]
    args += ["--entrypoint", "/bin/sh", fixture.IMAGE, "-c",
             "ffmpeg -v error -f lavfi -i color=s=32x32:d=0.2 -f lavfi -i anullsrc -shortest -c:v libx264 -c:a aac -y /fixture/media.mp4 && ffmpeg -v error -i /fixture/media.mp4 -frames:v 1 -threads 1 -y /fixture/media.png && /opt/engines/bin/python3 /fixture/source.py & exec /app/server"]
    subprocess.check_output(args)
    report = {}
    try:
        settings = fixture.wait(lambda: fixture.request("/settings"), bool)
        settings["debug"]["test_item_limit"] = 10
        settings["pipeline"]["youtube"]["min_interval_seconds"] = .02
        settings["pipeline"]["youtube"]["max_requests"] = 1000
        # 验收同时检查早停与补全日志，窗口需覆盖两轮操作及媒体下载诊断。
        settings["ui"]["task_log_lines"] = 1000
        settings["youtube"].update(video=True, format="best", metadata=True, comments=False,
                                   subtitles=False, auto_subtitles=False, thumbnail=False, extra_audio=False, extract_audio=False)
        fixture.request("/settings", "PUT", settings)
        for platform, url in (("instagram", "https://www.instagram.com/author/"), ("youtube", "https://www.youtube.com/@author")):
            (RUN / "data/fixture-items.json").write_text('["cached"]')
            sub = fixture.request("/subscriptions", "POST", {"platform": platform, "url": url, "display_name": platform, "enabled": False})
            scan(sub, "full")
            cached = next(c for c in fixture.request("/contents") if c["subscription_id"] == sub["id"] and c["remote_id"] in ("cached", "cached-1"))
            media = next(p for p in cached["paths"] if p.endswith((".png", ".mp4")))
            missing = RUN / media.lstrip("/")
            missing.unlink()
            (RUN / "data/fixture-items.json").write_text('["new2","new1","cached","oldgap","older"]')
            before = counts()
            scan(sub, "scan")
            incremental = checked(platform, before)
            assert incremental == {"new2": 1, "new1": 1, "cached": 1}, incremental
            assert not missing.exists(), "增量不应恢复旧媒体文件"
            assert not any(c["subscription_id"] == sub["id"] and c["remote_id"].startswith("oldgap") for c in fixture.request("/contents"))
            before = counts()
            scan(sub, "scan")
            unchanged = checked(platform, before)
            assert unchanged == {"new2": 1}, unchanged
            before = counts()
            scan(sub, "catchup")
            catchup = checked(platform, before)
            assert catchup == {code: 1 for code in ["new2", "new1", "cached", "oldgap", "older"]}, catchup
            assert missing.exists(), "补全没有恢复缺失文件"
            log = fixture.request(f"/subscriptions/{sub['id']}/log")["log"]
            assert "增量扫描命中本地缓存" in log and "[扫描汇总]" in log, log
            report[platform] = {"incremental_checked": incremental, "no_new_checked": unchanged,
                                "catchup_checked": catchup, "catchup_restores_missing_file": True}
            print(f"PASS {platform}: 增量只检查新项和缓存边界；无新增只检查首项；补全检查历史并恢复缺失文件", flush=True)
        (RUN / "report.json").write_text(json.dumps(report, ensure_ascii=False, indent=2))
        print("容器验收结果：" + str(RUN / "report.json"), flush=True)
        if os.environ.get("SMOKE_UI_HOLD") == "1":
            # 手动观察正式界面时保留隔离容器，常规验收不引入等待。
            print("UI_HOLD " + str(RUN), flush=True)
            deadline = time.monotonic() + 60
            while time.monotonic() < deadline and not (RUN / "continue-ui").exists():
                time.sleep(.2)
    except Exception:
        print(subprocess.check_output(["docker", "logs", NAME], text=True))
        raise
    finally:
        subprocess.run(["docker", "rm", "-f", NAME], stdout=subprocess.DEVNULL, check=False)


if __name__ == "__main__":
    run()

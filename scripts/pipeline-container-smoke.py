"""正式镜像与真实 SDK：十条范围内验证暂停生产、双消费者与平台滑动窗口。"""
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import time

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('incremental', ROOT / 'scripts/incremental-container-smoke.py')
# 复用素材注册，业务二进制、SDK 和 HTTP 下载器保持原实现。
source = importlib.util.module_from_spec(spec)
spec.loader.exec_module(source)
api = source.fixture
RUN = ROOT / '.test-runtime/pipeline' / str(time.time_ns())
NAME = 'media-harbor-pipeline-' + RUN.name
api.BASE = 'http://127.0.0.1:18883/api'

# 上游记录实际请求与同时连接数；媒体响应可独立释放，解析响应只延迟50ms。
HTTP = source.SOURCE.replace('import json,threading', 'import json,threading,time')
HTTP = HTTP.replace('counts=Counter(); lock=threading.Lock()', '''counts=Counter(); lock=threading.Lock()
events=[]; active=Counter(); peak=Counter()
def record(path, enter):
    platform=path.split('/')[2]
    lane='media' if path.startswith('/media/') else 'producer'
    key=platform+'-'+lane
    with lock:
        active[key]+=1 if enter else -1
        peak[key]=max(peak[key],active[key])
        if enter: events.append({'path':path,'at':time.monotonic()})
        Path('/data/events.json').write_text(json.dumps({'events':events,'peak':peak}))
''')
HTTP = HTTP.replace("        if self.path.startswith('/profile/'):", """        record(self.path, True)
        if self.path.startswith('/media/') and '/avatar/' not in self.path:
            while not Path('/data/release-media').exists(): time.sleep(.02)
        elif not self.path.startswith('/media/'): time.sleep(.05)
        record(self.path, False)
        if self.path.startswith('/profile/'):""")


def media_tasks(sub):
    return [t for t in api.request('/tasks') if t['subscription_id'] == sub['id'] and t['content_id']]


def wait_backlog(sub, capacity):
    # 下载线程满载时，容量限制应只统计等待任务，扫描仍保持运行状态。
    return api.wait(lambda: media_tasks(sub), lambda ts: sum(t['status']=='running' for t in ts)==2
                    and sum(t['status']=='queued' for t in ts)==capacity)


def run():
    for name in ('data', 'downloads', 'logs', 'archive', 'fixture/bin'):
        (RUN / name).mkdir(parents=True)
    (RUN / 'fixture/source.py').write_text(HTTP)
    (RUN / 'data/fixture-items.json').write_text(json.dumps(['p5','p4','p3','p2','p1']))
    # 素材仍走原 SDK 的 HTTP 后端，生产与媒体请求都会经过 Go 控制协议。
    for name, script in (('python3', source.INSTAGRAM), ('gallery-dl', source.INSTAGRAM), ('yt-dlp', source.YOUTUBE)):
        path = RUN / 'fixture/bin' / name
        path.write_text(script); path.chmod(0o755)
    args = ['docker','run','-d','--rm','--name',NAME,'--user',f'{os.getuid()}:{os.getgid()}',
            '-p','127.0.0.1:18883:8080','-e','INSTAGRAM_REQUEST_INTERVAL_SECONDS=0.02',
            '-e','PATH=/fixture/bin:/opt/engines/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin']
    for name in ('data','downloads','logs','archive','fixture'): args += ['-v',f'{RUN/name}:/{name}']
    args += ['--entrypoint','/bin/sh',api.IMAGE,'-c',
             'ffmpeg -v error -f lavfi -i color=s=32x32:d=0.2 -f lavfi -i anullsrc -shortest -c:v libx264 -c:a aac -y /fixture/media.mp4 && ffmpeg -v error -i /fixture/media.mp4 -frames:v 1 -threads 1 -y /fixture/media.png && /opt/engines/bin/python3 /fixture/source.py & exec /app/server']
    subprocess.check_output(args)
    try:
        settings = api.wait(lambda: api.request('/settings'), bool)
        assert settings['pipeline']['queue_capacity'] == 10, settings
        settings['debug']['test_item_limit'] = 10
        settings['instagram_concurrent'] = settings['youtube_concurrent'] = 2
        # 缩小验收容量，在十条调试范围内可靠触发背压，不枚举历史。
        settings['pipeline']['queue_capacity'] = 2
        for platform in ('instagram','youtube'):
            settings['pipeline'][platform].update(min_interval_seconds=.02, max_requests=1000)
        settings['youtube'].update(video=True,format='best',fragments=1,comments=False,metadata=True,
                                  subtitles=False,auto_subtitles=False,thumbnail=False,extra_audio=False,extract_audio=False)
        api.request('/settings','PUT',settings)
        subs={}
        scans={}
        for platform, url in (('instagram','https://www.instagram.com/author/'),('youtube','https://www.youtube.com/@author')):
            subs[platform]=api.request('/subscriptions','POST',{'platform':platform,'url':url,'display_name':platform,'enabled':False})
            scans[platform]=api.request(f"/subscriptions/{subs[platform]['id']}/scan?kind=full",'POST')
        for sub in subs.values(): wait_backlog(sub,2)
        # 消费者仍阻塞，两个快照之间不应出现新请求。
        before=json.loads((RUN/'data/requests.json').read_text())
        time.sleep(.4)
        after=json.loads((RUN/'data/requests.json').read_text())
        assert before==after, (before,after)
        # 实际连接峰值证明下载并行而元数据串行。
        peaks=json.loads((RUN/'data/events.json').read_text())['peak']
        for platform in subs:
            assert peaks[platform+'-producer']==1 and peaks[platform+'-media']==2, peaks
        assert not after.get('/visit/youtube/p1') and not after.get('/post/instagram/p3'), after
        print('PASS 每个平台单生产者、两个实际下载连接；两条等待队列满后停止下一条获取',flush=True)
        # 设置扩大容量后无需重启，两个解析器各自继续生产一条。
        settings['pipeline']['queue_capacity']=3
        api.request('/settings','PUT',settings)
        for sub in subs.values(): wait_backlog(sub,3)
        (RUN/'data/release-media').touch()
        api.wait(lambda: api.request('/tasks'),lambda ts: all(t['status']=='completed' for t in ts))
        # 主视频与信息文件不能各自再次访问帖子接口。
        requests=json.loads((RUN/'data/requests.json').read_text())
        assert all(requests['/post/youtube/'+code]==1 for code in ['p5','p4','p3','p2','p1']), requests
        print('PASS 调整队列容量立即恢复；YouTube 每条视频只解析一次，消费者复用完整格式信息',flush=True)
        # 保存更短的滑动窗口，保留已有请求时钟；以实际 HTTP 时间证明每秒至多两次。
        settings['pipeline']['youtube'].update(window_seconds=1,max_requests=2,min_interval_seconds=0)
        settings['pipeline']['queue_capacity']=10
        api.request('/settings','PUT',settings)
        (RUN/'data/fixture-items.json').write_text('["window2","window1"]')
        # 容器与宿主单调时钟原点不同，只使用容器事件次序。
        baseline=len(json.loads((RUN/'data/events.json').read_text())['events'])
        task=api.request(f"/subscriptions/{subs['youtube']['id']}/scan?kind=scan",'POST')
        api.wait(lambda: api.request('/tasks'),lambda ts: all(t['status']=='completed' for t in ts))
        events=json.loads((RUN/'data/events.json').read_text())['events']
        times=[e['at'] for e in events[baseline:] if '/youtube' in e['path'] and not e['path'].startswith('/media/')]
        # 两个请求之间可很近，但任意第三个请求都必须跨过滑动窗口边界。
        assert len(times)>=5 and all(b-a>=.94 for a,b in zip(times,times[2:])),times
        log=api.request(f"/subscriptions/{subs['youtube']['id']}/log")['log']
        assert '生产者滑动窗口已满' in log,log
        report={'default_queue_capacity':10,'tested_queue_capacity':2,'producer_peak':1,'consumer_peak':2,
                'stopped_next_fetch':True,'settings_resume_without_restart':True,'youtube_parse_count_per_video':1,
                'sliding_window_seconds':1,'sliding_window_requests':2,'request_times':times}
        (RUN/'report.json').write_text(json.dumps(report,ensure_ascii=False,indent=2))
        print('PASS YouTube 滑动窗口每秒至多两次真实解析请求；报告：'+str(RUN/'report.json'),flush=True)
    except Exception:
        print(subprocess.check_output(['docker','logs',NAME],text=True))
        raise
    finally:
        # 隔离状态不触及本地真实任务，退出后关闭容器与素材端口。
        subprocess.run(['docker','rm','-f',NAME],stdout=subprocess.DEVNULL,check=False)


if __name__=='__main__': run()

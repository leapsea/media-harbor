"""正式镜像和真实 gallery-dl：新头像后缀、旧文件迁移及归档读取。"""
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import time
import urllib.request

ROOT=Path(__file__).resolve().parents[1]
spec=importlib.util.spec_from_file_location('incremental',ROOT/'scripts/incremental-container-smoke.py')
source=importlib.util.module_from_spec(spec)
spec.loader.exec_module(source)
api=source.fixture
RUN=ROOT/'.test-runtime/avatar'/str(time.time_ns())
NAME='media-harbor-avatar-'+RUN.name
api.BASE='http://127.0.0.1:18883/api'


# 同源接口应根据真实文件返回图片类型，URL保持原来的稳定形式。
def read_avatar(sub):
    with urllib.request.urlopen(api.BASE+f"/subscriptions/{sub['id']}/avatar") as response:
        return response.headers.get('Content-Type'),response.read()


def run():
    # 所有素材与数据库都在隔离挂载中，不使用真实账号或已有下载。
    for name in ('data','downloads','logs','archive','fixture/bin'):(RUN/name).mkdir(parents=True)
    (RUN/'fixture/source.py').write_text(source.SOURCE)
    (RUN/'data/fixture-items.json').write_text('["one"]')
    for name,script in (('python3',source.INSTAGRAM),('gallery-dl',source.INSTAGRAM)):
        path=RUN/'fixture/bin'/name
        path.write_text(script);path.chmod(0o755)
    args=['docker','run','-d','--rm','--name',NAME,'--user',f'{os.getuid()}:{os.getgid()}',
          '-p','127.0.0.1:18883:8080','-e','INSTAGRAM_REQUEST_INTERVAL_SECONDS=0.02',
          '-e','PATH=/fixture/bin:/opt/engines/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin']
    for name in ('data','downloads','logs','archive','fixture'):args+=['-v',f'{RUN/name}:/{name}']
    # 使用FFmpeg产生有效PNG，接口响应可由实际图片解码器读取。
    args+=['--entrypoint','/bin/sh',api.IMAGE,'-c',
           'ffmpeg -v error -f lavfi -i color=s=32x32:d=0.2 -f lavfi -i anullsrc -shortest -c:v libx264 -c:a aac -y /fixture/media.mp4 && ffmpeg -v error -i /fixture/media.mp4 -frames:v 1 -threads 1 -y /fixture/media.png && /opt/engines/bin/python3 /fixture/source.py & exec /app/server']
    subprocess.check_output(args)
    try:
        settings=api.wait(lambda:api.request('/settings'),bool)
        settings['debug']['test_item_limit']=10
        api.request('/settings','PUT',settings)
        sub=api.request('/subscriptions','POST',{'platform':'instagram','url':'https://www.instagram.com/author/','display_name':'头像格式验证','enabled':False})
        # 新下载必须直接使用真实后缀，无需重启或刷新配置。
        files=list((RUN/'downloads/avatars/instagram').iterdir())
        assert len(files)==1 and files[0].suffix=='.png',files
        downloaded=files[0]
        mime,body=read_avatar(sub)
        assert mime=='image/png' and body==(RUN/'fixture/media.png').read_bytes(),mime
        assert api.request('/tasks')==[], '主页创建不应启动媒体任务'
        print('PASS 原SDK下载有效PNG；存储后缀.png，头像接口image/png',flush=True)
        # 将测试头像还原为旧布局，模拟升级前downloads与archive两处头像。
        stamp=downloaded.stat().st_mtime_ns
        old=downloaded.with_suffix('.img');downloaded.rename(old)
        archive=RUN/'archive/avatars/instagram'/old.name
        archive.parent.mkdir(parents=True);archive.write_bytes(body)
        os.utime(archive,ns=(stamp,stamp))
        # 上游计数用于确认迁移没有触发额外下载。
        requests=(RUN/'data/requests.json').read_bytes()
        # 重启必须只重命名，不能通过网络刷新头像掩盖迁移问题。
        subprocess.check_output(['docker','restart',NAME])
        api.wait(lambda:api.request('/settings'),bool)
        for legacy in (old,archive):
            actual=legacy.with_suffix('.png')
            api.wait(lambda:actual.exists(),bool)
            assert not legacy.exists() and actual.read_bytes()==body and actual.stat().st_mtime_ns==stamp,actual
        assert (RUN/'data/requests.json').read_bytes()==requests,'迁移触发平台请求'
        assert read_avatar(sub)==('image/png',body)
        print('PASS downloads与archive旧.img原地迁移.png；内容、mtime和平台请求数不变',flush=True)
        # 独立验证归档位置查找，不让downloads文件遮蔽读取结果。
        old.with_suffix('.png').unlink()
        # Docker共享挂载的删除可异步可见，等待容器改从归档位置读取。
        api.wait(lambda:read_avatar(sub),lambda result:result==('image/png',body))
        logs=subprocess.check_output(['docker','logs',NAME],text=True,stderr=subprocess.STDOUT)
        assert '头像后缀已更新' in logs and 'avatar-migration' in logs,logs
        report={'new_extension':'.png','content_type':'image/png','legacy_migrated':True,'preserves_bytes_and_mtime':True,
                'no_platform_requests_for_migration':True,'archive_avatar_readable':True}
        (RUN/'report.json').write_text(json.dumps(report,ensure_ascii=False,indent=2))
        print('PASS 接口读取归档中的正确后缀头像；报告：'+str(RUN/'report.json'),flush=True)
    except Exception:
        print(subprocess.check_output(['docker','logs',NAME],text=True,stderr=subprocess.STDOUT))
        raise
    finally:
        # 清理仅限本次测试容器，素材目录保留供复核。
        subprocess.run(['docker','rm','-f',NAME],stdout=subprocess.DEVNULL,check=False)


if __name__=='__main__':run()

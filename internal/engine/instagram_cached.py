"""已解析媒体继续交给 gallery-dl 下载，沿用原会话、分片与断点逻辑。"""
import json
import re
import sys
from datetime import datetime


def install_instagram_source(path):
    from gallery_dl import extractor
    from gallery_dl.extractor.common import Extractor, Message

    with open(path, encoding="utf-8") as stream:
        source = json.load(stream)
    original = extractor.find

    class MediaSource(Extractor):
        category = "instagram"
        subcategory = "media-source"
        cookies_domain = ".instagram.com"
        directory_fmt = ()
        filename_fmt = "{post_id}_{num}.{extension}"

        def items(self):
            # 仅跳过已经完成的帖子解析，媒体下载仍使用原下载器和请求头。
            metadata = source["metadata"]
            # JSON 中日期为字符串，恢复原提取器的日期对象以兼容已有后处理配置。
            for key in ("date", "post_date"):
                if isinstance(metadata.get(key), str):
                    try:
                        metadata[key] = datetime.fromisoformat(metadata[key])
                    except ValueError:
                        pass
            yield Message.Directory, "", metadata
            yield Message.Url, source["url"], metadata

    def find(address):
        if address == source["post_url"]:
            return MediaSource(re.match(".*", address))
        return original(address)

    extractor.find = find


install_instagram_source(sys.argv.pop(1))
install_instagram_progress()
import gallery_dl
# main 返回退出码，必须显式传给父进程，下载失败不能误报为成功。
sys.exit(gallery_dl.main())

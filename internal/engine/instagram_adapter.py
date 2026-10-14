"""详情接口重定向时，复用帖子网页中的原始媒体对象。"""
import json
import re
import time
import os


def instagram_web_post(page, shortcode):
    # 网页模型解析已经由 Go 接管；SDK 适配只交换原始页面和解析结果。
    if os.environ.get("MEDIA_HARBOR_CONTROL"):
        return control({"Kind": "web-post", "Page": page, "Shortcode": shortcode})
    return legacy_instagram_web_post(page, shortcode)


def legacy_instagram_web_post(page, shortcode):
    # 历史实现仅供无控制通道的独立 SDK 适配测试参考，正式入口均调用 Go。
    # 只读取当前短码的完整媒体对象，排除路由参数、登录者与推荐帖子。
    def find(value):
        if isinstance(value, dict):
            if (value.get("code") == shortcode and value.get("pk")
                    and value.get("user") and value.get("image_versions2")):
                return value
            for child in value.values():
                post = find(child)
                if post:
                    return post
        elif isinstance(value, list):
            for child in value:
                post = find(child)
                if post:
                    return post

    for script in re.findall(r"<script\b[^>]*>(.*?)</script\s*>", page, re.I | re.S):
        try:
            value = json.loads(script)
        except ValueError:
            continue
        post = find(value)
        if post:
            return post


def install_instagram_adapter():
    from gallery_dl import exception
    from gallery_dl.extractor.instagram import InstagramAPI

    original_media = InstagramAPI.media

    def media(self, shortcode):
        started = time.monotonic()
        self.extractor.log.info("帖子 %s：开始请求媒体接口", shortcode)
        try:
            for post in original_media(self, shortcode):
                self.extractor.log.info("帖子 %s：媒体接口解析成功；耗时 %.3fs；媒体 %s 项", shortcode,
                                       time.monotonic()-started, len(post.get("carousel_media") or [post]))
                yield post
            return
        except exception.AbortExtraction as error:
            if "HTTP redirect to home page" not in str(error):
                raise
            extraction_error = error

        # 保留同一个引擎会话与 Cookie，仅为网页导航补齐请求头。
        extractor = self.extractor
        address = f"{extractor.root}/p/{shortcode}/"
        headers = {
            "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7",
            "Accept-Language": "zh-CN,zh;q=0.9",
            "Referer": address,
            "Sec-Fetch-Dest": "document",
            "Sec-Fetch-Mode": "navigate",
            "Sec-Fetch-Site": "same-origin",
            "Sec-Fetch-User": "?1",
            "Upgrade-Insecure-Requests": "1",
        }
        extractor.log.info("帖子 %s：媒体接口重定向，改从帖子网页解析", shortcode)
        response = extractor.request(address, headers=headers)
        extractor.log.info("帖子 %s：网页响应状态 %s；接口与网页累计耗时 %.3fs", shortcode,
                           response.status_code, time.monotonic()-started)
        post = instagram_web_post(response.text, shortcode)
        if not post:
            raise exception.AbortExtraction(
                f"帖子 {shortcode}：网页未返回有效媒体；原接口错误：{extraction_error}")
        count = len(post.get("carousel_media") or [post])
        extractor.log.info("帖子 %s：网页解析成功；作者 %s；媒体 %s 项",
                           shortcode, post["user"].get("username", ""), count)
        yield post

    InstagramAPI.media = media

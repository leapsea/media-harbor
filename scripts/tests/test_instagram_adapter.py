"""离线验证详情网页补解析，覆盖推荐帖子、轮播与异常边界。"""
import importlib.util
import json
from pathlib import Path
import sys
from types import SimpleNamespace
import unittest
from unittest.mock import patch

SOURCE = Path(__file__).resolve().parents[2] / "internal/engine/instagram_adapter.py"
spec = importlib.util.spec_from_file_location("instagram_adapter", SOURCE)
adapter = importlib.util.module_from_spec(spec)
spec.loader.exec_module(adapter)


class AbortExtraction(Exception):
    pass


class InstagramAdapterTest(unittest.TestCase):
    def setUp(self):
        self.post = {"code": "target", "pk": "123", "user": {"username": "author"},
                     "image_versions2": {"candidates": [{"url": "https://example.com/image"}]},
                     "carousel_media": [{"pk": "1"}, {"pk": "2"}]}
        self.page = "<script>not JSON</script><script type='application/json'>" + json.dumps({
            "route": {"shortcode": "target"},
            "recommended": {**self.post, "code": "other"},
            "require": [{"__bbox": {"result": {"items": [self.post]}}}],
        }) + "</script>"

    def test_exact_post_with_carousel(self):
        self.assertEqual(adapter.instagram_web_post(self.page, "target"), self.post)
        self.assertIsNone(adapter.instagram_web_post(self.page, "absent"))

    def test_missing_media_is_not_success(self):
        self.assertIsNone(adapter.instagram_web_post(
            '<script>{"code":"target","pk":"123","user":{"username":"author"}}</script>', "target"))

    def install(self, original, page):
        api = type("API", (), {"media": original})
        modules = {
            "gallery_dl": SimpleNamespace(exception=SimpleNamespace(AbortExtraction=AbortExtraction)),
            "gallery_dl.extractor": SimpleNamespace(),
            "gallery_dl.extractor.instagram": SimpleNamespace(InstagramAPI=api),
        }
        with patch.dict(sys.modules, modules):
            adapter.install_instagram_adapter()
        client = api()
        client.extractor = SimpleNamespace(root="https://www.instagram.com",
            log=SimpleNamespace(info=lambda *args: None),
            # 模拟真实 HTTP 响应，为正文解析与新增状态日志同时提供所需字段。
            request=lambda address, **kwargs: SimpleNamespace(text=page, status_code=200))
        return client, AbortExtraction

    def test_redirect_falls_back_to_webpage(self):
        # 原接口的失败由生成器抛出，与 gallery-dl 的分页阶段一致。
        def fail(client, shortcode):
            raise AbortExtraction("HTTP redirect to home page")
        client, _ = self.install(fail, self.page)
        self.assertEqual(list(client.media("target")), [self.post])

    def test_login_error_is_preserved(self):
        def fail(client, shortcode):
            raise AbortExtraction("HTTP redirect to login page")
        client, _ = self.install(fail, self.page)
        with self.assertRaisesRegex(AbortExtraction, "login page"):
            list(client.media("target"))

    def test_empty_webpage_preserves_failure(self):
        def fail(client, shortcode):
            raise AbortExtraction("HTTP redirect to home page")
        client, _ = self.install(fail, "<html></html>")
        with self.assertRaisesRegex(AbortExtraction, "网页未返回有效媒体"):
            list(client.media("target"))

    def test_normal_interface_remains_unchanged(self):
        client, _ = self.install(lambda client, shortcode: iter([self.post]), "")
        self.assertEqual(list(client.media("target")), [self.post])


if __name__ == "__main__":
    unittest.main()

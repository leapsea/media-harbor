-- 当前版本的初始化结构；关系与状态聚合由仓储事务维护。
-- subscriptions 为订阅配置，post_tasks 为帖子任务，download_items 为下载资源任务。
-- content_items 保存媒体文件集合，tasks 保存队列与执行尝试。
-- 此文件不包含版本迁移、外键或触发器。
PRAGMA journal_mode=WAL;
CREATE TABLE IF NOT EXISTS subscriptions(id INTEGER PRIMARY KEY,platform TEXT NOT NULL,identity TEXT NOT NULL,payload TEXT NOT NULL,UNIQUE(platform,identity));
CREATE TABLE IF NOT EXISTS content_items(id INTEGER PRIMARY KEY,subscription_id INTEGER NOT NULL,remote_id TEXT NOT NULL,payload TEXT NOT NULL,UNIQUE(subscription_id,remote_id));
CREATE TABLE IF NOT EXISTS post_tasks(id INTEGER PRIMARY KEY,subscription_id INTEGER NOT NULL,remote_id TEXT NOT NULL,payload TEXT NOT NULL,UNIQUE(subscription_id,remote_id));
CREATE TABLE IF NOT EXISTS download_items(id INTEGER PRIMARY KEY,post_id INTEGER NOT NULL,content_id INTEGER NOT NULL,kind TEXT NOT NULL,payload TEXT NOT NULL,UNIQUE(post_id,content_id,kind));
CREATE INDEX IF NOT EXISTS download_items_content ON download_items(content_id);
CREATE TABLE IF NOT EXISTS download_history_cleared(content_id INTEGER PRIMARY KEY);
CREATE TABLE IF NOT EXISTS tasks(id INTEGER PRIMARY KEY,subscription_id INTEGER NOT NULL,content_id INTEGER NOT NULL,kind TEXT NOT NULL,status TEXT NOT NULL,payload TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS tasks_status ON tasks(status,id);
CREATE INDEX IF NOT EXISTS tasks_subscription_id ON tasks(subscription_id,id);
-- 详情按配置聚合每条媒体的最新尝试，覆盖索引避免反复扫描全站执行账本。
CREATE INDEX IF NOT EXISTS tasks_subscription_content ON tasks(subscription_id,content_id,id);
CREATE UNIQUE INDEX IF NOT EXISTS task_active ON tasks(subscription_id,content_id,kind) WHERE status IN ('queued','running');
CREATE TABLE IF NOT EXISTS app_settings(id INTEGER PRIMARY KEY,payload TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS engine_versions(name TEXT PRIMARY KEY,version TEXT NOT NULL,checked_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS platform_runs(platform TEXT PRIMARY KEY,payload TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS youtube_paid_blocks(channel_id TEXT PRIMARY KEY,failed_task_id INTEGER NOT NULL);

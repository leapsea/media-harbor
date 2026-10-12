Media Harbor — 产品需求与技术设计
1. 项目目标

开发一个单用户、自托管的媒体订阅下载系统，支持 Instagram 和 YouTube，提供 Web Dashboard、定时订阅、下载历史管理、目录模板、自动归档和 Docker 部署。

参考项目：

https://github.com/gdluxx/gdluxx
https://github.com/qx6ghqkz/gallery-dl-server

优先复用现有项目中可用的 Dashboard、下载引擎集成和 Docker 部署能力。开发前需确认代码许可证、API 和可复用范围。

2. 技术选型
后端：Go
前端：Vue 3 + TypeScript
数据库：SQLite
下载引擎：gallery-dl、yt-dlp
媒体处理：FFmpeg
部署：Docker Compose
镜像发布：GitHub Actions + GitHub Container Registry（GHCR）

采用单实例架构，后台执行下载、定时调度和归档任务。不考虑多用户、复杂权限系统及开发阶段的数据库向后兼容。

3. Dashboard
3.1 首页

展示：

订阅数量及运行状态。
待执行、运行中、已完成和失败的任务。
最近下载记录。
gallery-dl、yt-dlp、FFmpeg 当前版本及更新状态。
运行数据、下载目录和归档目录的挂载及权限状态。
3.2 订阅与任务管理

支持：

在线添加、编辑、暂停、恢复和删除订阅。
手动触发全量初始化或增量扫描。
配置定时更新周期。
查看下载历史、任务日志及失败原因。
取消任务、重试失败任务。
配置 Cookie、下载规则、并发限制和归档选项。

支持 Instagram 用户主页及 YouTube 频道、播放列表和视频等引擎支持的 URL。

3.3 系统设置

支持：

gallery-dl Cookie 配置。
yt-dlp Cookie 配置。
全局及各下载引擎的并发限制。
默认下载目录模板。
默认 YouTube 下载选项。
自动归档开关。
下载引擎版本检查与更新。
4. 下载引擎
4.1 gallery-dl
支持 Instagram 下载。
支持 Cookie 上传、替换、删除及配置校验。
支持配置文件在线编辑。
支持下载历史去重。
支持查看版本、检查更新和升级。
4.2 yt-dlp
支持 YouTube 视频、频道及播放列表。
支持 Cookie 配置。
支持下载归档与增量更新。
支持配置视频格式、字幕、评论和音频处理。
支持查看版本、检查更新和升级。
4.3 FFmpeg
支持音视频合并。
支持从已下载视频提取音频。
支持配置目标音频格式，例如 MP3、M4A、FLAC。
支持按需执行音频后处理。

FFmpeg 应作为独立版本管理的运行依赖，不应与 yt-dlp 的 Python 包版本混为一谈。

5. YouTube 下载选项

创建或编辑订阅时，可以配置以下选项：

选项	功能
视频下载	下载视频及音视频合并
字幕下载	选择语言、人工字幕和自动生成字幕
字幕嵌入	将支持的字幕嵌入视频
评论下载	尝试获取评论并保存为配置的文件格式
额外下载音频	在视频之外保留独立音频文件
FFmpeg 提取音频	从已下载的视频提取音频
音频格式与质量	配置输出格式和适用的质量参数
缩略图与元数据	按需保存缩略图及元数据文件

要求：

各选项可以独立启用或组合使用。
支持设置全局默认值及订阅级覆盖。
独立下载音频与本地提取音频是两种不同操作。
字幕、评论和格式支持取决于 yt-dlp 当前版本及目标内容的实际能力。
不支持的选项应记录明确错误，不能将整个系统判定为不可用。
下载成功但后处理失败时，应保留必要的中间文件，便于重试。
6. Cookie 管理

Dashboard 支持上传、替换、删除及校验 Cookie 文件。

要求：

Cookie 存放在运行数据目录中。
限制文件权限，仅允许应用运行用户读取。
不向前端返回 Cookie 内容。
不在日志和错误信息中输出 Cookie 原文。
更新 Cookie 时使用临时文件和原子替换。
Cookie 无效时显示认证错误，不进行无限重试。
7. 并发与限流

支持配置：

全局最大下载任务数。
yt-dlp 最大并发任务数。
gallery-dl 最大并发任务数。
单个订阅最大并发数。
单任务内部下载并发。
请求间隔、失败重试及指数退避。

所有并发限制必须由后端执行。

遇到 HTTP 429、认证错误或疑似限流时，应暂停或延迟相关任务，并提示用户检查访问权限、Cookie 或请求频率。

并发限制只能降低请求压力，不能保证不会被平台限流。

8. 订阅、全量初始化与增量更新
8.1 订阅配置

每个订阅保存：

平台和订阅 URL。
平台账号或频道 ID。
首次确定的目录名称。
全量初始化策略。
定时更新周期。
下载目录模板。
Cookie 配置。
YouTube 下载选项。
自动归档选项。

支持常用定时周期和自定义 Cron 表达式，并记录最近扫描时间、最近成功时间及下次执行时间。

8.2 全量初始化

首次初始化时，系统枚举当前可获取的历史内容，将内容记录到数据库并加入下载队列。

如果平台只返回部分历史内容或无法完整枚举，应记录扫描范围和错误，不得将部分扫描标记为完整成功。

8.3 增量更新

定时扫描时，通过平台内容 ID 与数据库历史记录去重，只下载新增内容。

已完成的内容不重复下载。
失败内容可以按策略重试。
同一订阅不得重复并行扫描。
任务及扫描状态必须持久化。
服务重启后恢复待执行任务，并检查状态不确定的任务。
9. 目录模板与稳定映射
9.1 目录模板

默认支持类似以下规则：

{platform}/{display_name}/{year}/{month}{day}/{filename}


示例：

instagram/alice/2026/10/11/image.jpg
youtube/example_channel/2026/10/11/video.mp4


支持变量：

{platform}：平台名称。
{subscription_id}：内部订阅 ID。
{display_name}：首次确定的固定名称。
{current_name}：平台当前名称。
{year}、{month}、{day}：日期。
{content_id}：平台内容 ID。
{filename}、{ext}：文件名和扩展名。

日期默认使用内容发布日期；无法获取时回退到下载日期。

9.2 名称映射

首次创建订阅时记录平台稳定 ID 和固定目录名称。

例如：

platform_user_id = 123456
current_name     = alice
display_name     = alice
directory_key    = instagram/alice


之后用户名变为 alice_new，目录仍为：

instagram/alice


规则：

平台稳定 ID 是订阅身份的主要依据。
display_name 以首次创建时的值为准，默认不可变。
用户名变化只更新 current_name。
同一平台的相同稳定 ID 不得重复创建订阅。
无法取得稳定 ID 时，可以规范化 URL 作为备用身份，并提示映射可靠性较低。
修改目录模板不得自动移动或重命名已有文件。
所有路径必须经过安全校验，禁止路径穿越。
10. 自动归档

支持开启或关闭自动归档。

默认目录：

/data       # 数据库、Cookie、配置和日志
/downloads  # 下载工作目录
/archive    # 最终归档目录


下载和后处理全部成功后，将文件迁移到归档目录，并更新数据库中的文件位置和归档状态。

同一文件系统优先使用原子重命名。
跨文件系统使用复制、校验、确认目标文件成功后再删除源文件。
归档失败时保留源文件，并允许重试。
默认移动文件，不额外保留副本。
不得将未完成的下载文件归档。
11. Docker 目录挂载与权限检测
11.1 目录行为
目录	默认路径	未挂载时的行为
运行数据	/data	使用容器内部目录，允许运行
下载数据	/downloads	使用容器内部目录，允许下载
归档数据	/archive	禁用归档功能

未配置目录挂载不得直接导致应用启动失败。

Dashboard 应提示：容器内部数据可能在容器重建后丢失。

11.2 启动检测

应用启动后，后端检测三个目录：

目录是否存在。
目录是否可读、可写。
是否能够创建和删除临时测试文件。
是否已挂载。
当前运行 UID/GID。
最近检测时间和错误原因。

目录挂载状态与权限状态必须分开检测。目录存在且可写，并不代表该目录已挂载。

对于无法可靠判断的挂载状态，显示 Unknown，不能直接判定为已挂载。

11.3 权限检测与处理

运行数据目录：

未挂载但可写：允许运行并显示警告。
不可写或数据库无法初始化：记录错误，禁止依赖数据库的服务进入正常运行状态。

下载目录：

未挂载但可写：允许下载，并显示数据可能丢失的警告。
不可写：禁止启动新的下载任务，但保留 Dashboard 和订阅管理功能。
恢复可写后，支持重新检测并恢复下载。

归档目录：

未挂载：自动禁用归档。
不可写：暂停归档任务。
恢复可用后：允许重新启用归档并继续处理待归档文件。

权限检测必须以实际应用运行 UID/GID 执行，不能使用 root 权限的检测结果代替。

检测测试文件应使用专用临时文件，并在检测完成后清理。

11.4 Dashboard 状态

每个目录显示：

容器内路径。
挂载状态：已挂载、未挂载或未知。
权限状态：正常、不可写或检测失败。
最近检测时间。
错误原因。
重新检测按钮。
11.5 日志

启动时记录目录检测结果，例如：

[INFO] Runtime directory: /data
[INFO] Runtime directory writable: true
[INFO] Runtime directory mounted: false

[INFO] Download directory: /downloads
[INFO] Download directory writable: true
[INFO] Download directory mounted: false

[WARN] Archive directory: /archive
[WARN] Archive directory mounted: false
[WARN] Archive feature disabled: archive directory is not mounted


目录状态变化、权限错误及重新检测结果也必须写入日志。不得记录 Cookie 内容。

11.6 归档选项

创建或编辑订阅时提供：

Disable：禁用归档，文件保留在下载目录。
Enable：下载成功后自动迁移到归档目录。

当归档目录未挂载或不可写时：

新建订阅默认选择 Disable。
允许正常创建订阅和下载任务。
Dashboard 显示归档不可用的原因。
不允许启用自动归档，直到目录检测通过。
已启用归档的订阅遇到目录异常时，暂停归档任务，但保留配置和待处理记录。
目录恢复正常后，重新检测并允许恢复归档。

如果用户主动启用归档后，目录后来失效，系统不得自动删除或清理源文件。

12. SQLite 数据库

至少包含以下表：

subscriptions：订阅 URL、平台 ID、固定名称、当前名称、调度周期及状态。
content_items：平台内容 ID、发布日期、下载状态、文件路径和归档状态。
tasks：任务类型、执行状态、重试次数、执行时间和错误信息。
app_settings：并发、目录模板、默认下载选项及归档策略。
engine_versions：下载引擎当前版本及更新记录。

使用唯一约束和索引防止重复订阅与重复下载。

开发阶段允许删除并重建数据库，但不得默认删除下载文件、归档文件和 Cookie。

13. Docker 部署

支持 Docker Compose、可配置端口、UID/GID 和独立目录挂载。

services:
  media-harbor:
    image: ghcr.io/<owner>/media-harbor:latest
    restart: unless-stopped

    ports:
      - "8080:8080"

    user: "1000:1000"

    volumes:
      - ./data:/data
      - ./downloads:/downloads
      - ./archive:/archive

    environment:
      GLOBAL_MAX_CONCURRENT: "2"
      YTDLP_MAX_CONCURRENT: "1"
      GALLERY_DL_MAX_CONCURRENT: "1"
      AUTO_ARCHIVE: "false"


要求：

支持 linux/amd64 和 linux/arm64。
提供 .env.example 和部署说明。
容器重建后，已挂载的数据应保留。
检查宿主机目录权限。
使用 Docker 原生 user 时，不得假设容器能够自动修改宿主机目录所有者。
14. 引擎更新与自动构建

支持 gallery-dl、yt-dlp 和 FFmpeg 的版本检查与更新。

推荐默认采用 GitHub Actions 构建新镜像，而不是在容器启动时无条件更新软件。

构建流程：

检查上游版本。
解析并固定目标版本。
构建 Docker 镜像。
执行启动、版本和基本功能测试。
测试通过后发布至 GHCR。

支持：

手动触发构建。
上游新版本触发候选构建。
linux/amd64、linux/arm64 多架构镜像。
记录应用、gallery-dl、yt-dlp 和 FFmpeg 的版本。
固定版本和回滚。
更新失败时保留现有稳定版本。

Dashboard 提供版本查看、检查更新和更新状态。

15. 开发阶段
基础框架： Go、Vue、SQLite、Docker、GitHub Actions。
下载能力： 两个下载引擎、FFmpeg、Cookie、YouTube 下载选项、并发控制。
订阅系统： 全量初始化、增量扫描、定时调度、下载历史及重启恢复。
文件管理： 路径模板、稳定名称映射、目录检测、自动归档。
发布流程： 引擎版本管理、自动构建、镜像发布及回滚。
16. 验收标准
Dashboard 可管理 Instagram 和 YouTube 订阅。
支持全量初始化、增量更新及定时执行。
Cookie 配置安全，任务状态可持久化。
YouTube 可按配置下载字幕、评论及独立音频，或使用 FFmpeg 提取音频。
并发限制在后端实际生效。
用户名变化不会改变固定目录映射。
归档失败不会丢失源文件。
三个目录均未挂载时，应用仍可启动。
运行数据、下载目录未挂载但可写时，系统仍可使用并显示警告。
下载目录不可写时，不启动新的下载任务。
归档目录未挂载或不可写时，自动禁用或暂停归档。
Dashboard 和日志显示挂载及权限检测结果。
目录恢复可用后，可重新检测并恢复对应功能。
Docker 支持端口、UID/GID 和三个独立目录挂载。
GitHub Actions 可以构建并发布指定版本的多架构镜像。
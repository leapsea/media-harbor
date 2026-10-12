<p align="center">
  <a href="README.md">简体中文</a> · <strong>English</strong> · <a href="README.hi.md">हिन्दी</a> · <a href="README.ru.md">Русский</a> · <a href="README.ja.md">日本語</a>
</p>

# Media Harbor

Download YouTube and Instagram content to your own device, organize it by creator and date, and keep up with new posts from creator profiles.

## What it does

- Download individual videos/posts or check profiles on a schedule.
- Save highest-quality MP4 and additional m4a audio from YouTube; choose subtitles, comments, thumbnails, and metadata.
- Skip existing content. Recover downloads reuses existing files, fills missing outputs, and refreshes selected comments and metadata.
- View task status, progress, logs, and download history; cancel, retry, and resume after a restart.
- Organize and archive files. The interface supports Chinese, English, Hindi, Russian, and Japanese.

## Screenshots

PC landscape previews from Docker, using demo data.

![Overview](docs/previews/dashboard.jpg)

<details>
<summary>View the other main pages</summary>

**Tasks**

![Tasks](docs/previews/tasks.jpg)

**Download history**

![Download history](docs/previews/history.jpg)

**Settings**

![Settings](docs/previews/settings.jpg)

</details>

## Start

### Option 1: Pull the image (recommended)

Install Docker; no source checkout is needed. After the image is first published, run:

```sh
docker pull ghcr.io/leapsea/media-harbor:latest
mkdir -p data downloads archive logs
```

On Linux, set folder ownership before starting the container: `sudo chown -R 1000:1000 data downloads archive logs`. The image runs as `1000:1000` by default.

```sh
docker run -d --name media-harbor \
  --restart unless-stopped \
  -p 8080:8080 \
  -v "$PWD/data:/data" \
  -v "$PWD/downloads:/downloads" \
  -v "$PWD/archive:/archive" \
  -v "$PWD/logs:/logs" \
  -e TZ=Asia/Singapore \
  ghcr.io/leapsea/media-harbor:latest
```

Open [http://localhost:8080](http://localhost:8080). This example mounts all folders. Remove the corresponding `-v` option if you do not need archiving or persistent logs.

| Host folder | Contents | Mount required? |
| --- | --- | --- |
| `data` | Tasks, settings, Cookies, and download index | Not required to start; recommended to retain state when recreating the container |
| `downloads` | Downloaded files | Not required to start; recommended to access and retain files on the host |
| `archive` | Archived files | Required for archiving, with write access |
| `logs` | Task logs | Optional; mount to access and retain logs on the host |

Unmounted contents stay inside the container and are lost when it is removed. Change the first port in `-p 8080:8080` to use another host port, and replace folder paths with your storage locations.

### Option 2: Build from source

Install Docker, Compose, and Git, then run:

```sh
git clone https://github.com/leapsea/media-harbor.git
cd media-harbor
cp .env.example .env
mkdir -p data logs downloads archive
```

On Linux, set folder ownership before starting, using `PUID` / `PGID` from `.env`. For the defaults, run `sudo chown -R 1000:1000 data logs downloads archive`. See [deployment](docs/部署与开发.md#docker-配置) for custom users.

```sh
docker compose up -d --build
```

## Create your first task

### Download one video or post

1. Open **Tasks → New task**.
2. Select the platform, paste the content URL, and confirm the resolved folder name.
3. Choose download options and save. An individual content URL runs once immediately.
4. Follow progress in Tasks. After completion, view the outputs in Download history and copy their folder path.

### Follow a creator profile

1. Create a task with a profile URL, folder name, and interval, then save.
2. Select **Full initialization** to download currently available content. Saving a profile task does not start it.
3. Scheduling starts after the first manual run. Later scans queue only new content; pause or resume the schedule as needed.

| Action | When to use it |
| --- | --- |
| Full initialization | Fetch profile content for the first time |
| Scan new items | Check for new content; also works for the first run |
| Recover downloads | Reuse existing files, fetch missing outputs, refresh selected comments and metadata |
| Retry | Run failed tasks again |

History tags describe actual output files. Comments are stored in metadata JSON and share that file with the metadata tag. **Clear download history** removes display records while retaining files and the download index, so later scans still skip existing content.

## Common settings

Change settings under **Settings**, then select **Save settings**.

| Section | Purpose |
| --- | --- |
| Downloads | Platform concurrency and default YouTube outputs; individual tasks can override download options |
| Folder rules | Folder template, date source, and archiving; archiving needs a writable mount |
| Cookie | Paste browser Cookies in the editor; saved Cookies are used automatically |
| Interface | Language, recent download count, and list page sizes |
| Storage | Usage, log clearing, and cleanup of completed or orphaned task cache |
| Debug | Limit items to try a small batch; disable the limit for full processing |

The default folder template is `{platform}/{directory_name}/{year}/{month}{day}/{filename}`. Dates use upload/publication time when available, otherwise task creation time, in the local timezone.

For direct image use, set ports, paths, and timezone in the run options; for source-based Compose deployment, edit `.env`. Check task logs when platform access fails; results depend on network access and the content the platform makes available.

## Documentation

The detailed documents below are in Chinese.

- [Implemented in v1.0.0](docs/versions/v1.0.0.md)
- [Deployment, development, and releases](docs/部署与开发.md)
- [Architecture](docs/V1-架构.md)

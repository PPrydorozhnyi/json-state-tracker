# State Tracker

A lightweight tool that polls a JSON API or HTML page, extracts values at a configurable path, and sends a Telegram notification when the set of values changes (new items appear or existing ones disappear).

Works anywhere: run it locally with cron, launchd, Task Scheduler, or any other scheduler — or fork this repo and let GitHub Actions handle it for free.

## How it works

1. Fetches a response from `TARGET_ENDPOINT` (JSON or HTML)
2. Extracts values using `TRACK_PATH` — a [gjson](https://github.com/tidwall/gjson) path for JSON, or a CSS selector for HTML
3. Compares the extracted set against the previously saved state
4. Sends a Telegram message listing added/removed values (if any)
5. Saves the new set to `last_response.json`

## Configuration

All configuration is done via environment variables. When running on GitHub Actions, set these as **repository secrets** (Settings > Secrets and variables > Actions).

### Required

| Variable | Description |
|---|---|
| `TARGET_ENDPOINT` | Full URL to poll (JSON API or HTML page) |
| `TELEGRAM_BOT_TOKEN` | Telegram bot token (from [@BotFather](https://t.me/BotFather)) |
| `TELEGRAM_CHAT_ID` | Telegram chat/group ID to send notifications to |
| `TRACK_PATH` | gjson path (JSON) or CSS selector (HTML) that extracts the values to track. Append `@attr` to extract an attribute instead of text (see below) |

### Optional

| Variable | Description |
|---|---|
| `REQUEST_HEADERS` | JSON object of HTTP headers to include in the request (see below) |

## TRACK_PATH examples (JSON)

The `TRACK_PATH` variable uses [gjson syntax](https://github.com/tidwall/gjson/blob/master/SYNTAX.md) when the response is JSON. The `#` operator iterates arrays.

Given this API response:

```json
{
  "events": [
    { "id": 1, "event_date": "2026-03-15", "title": "Concert A" },
    { "id": 2, "event_date": "2026-04-01", "title": "Concert B" },
    { "id": 3, "event_date": "2026-05-20", "title": "Workshop C" }
  ]
}
```

| TRACK_PATH | Extracted values |
|---|---|
| `events.#.event_date` | `2026-03-15`, `2026-04-01`, `2026-05-20` |
| `events.#.id` | `1`, `2`, `3` |
| `events.#.title` | `Concert A`, `Concert B`, `Workshop C` |

For nested structures like `{"data": {"items": [...]}}`, use `data.items.#.field_name`.

See the full [gjson path syntax documentation](https://github.com/tidwall/gjson/blob/master/SYNTAX.md) for advanced queries, filtering, and modifiers.

## TRACK_PATH examples (HTML)

When the response is HTML (auto-detected from the `Content-Type` header), `TRACK_PATH` is a CSS selector. The tool finds all matching elements and collects their trimmed text content.

Append `@attr` to extract an attribute value instead of text:

| TRACK_PATH | What it extracts |
|---|---|
| `.title a` | text content of each `<a>` inside `.title` |
| `.title a@href` | `href` attribute of each `<a>` inside `.title` |
| `div[class*=showDate-]@class` | `class` attribute of each matching `<div>` |

**Example — tracking opera schedule on opera.com.ua:**

```html
<div class="right_part">
  <div class="title"><a href="/afisha/madam-batterflay-chio-chio-san-50">Мадам Баттерфлай (Чіо-Чіо-сан)</a></div>
</div>
```

```bash
export TARGET_ENDPOINT="https://opera.com.ua/afisha?month=01-03-2026"
export TRACK_PATH=".right_part .title a"
```

**Example — tracking event dates on kontramarka.ua:**

```html
<div class="spoiler showDate-2026-03-05">...</div>
<div class="spoiler showDate-2026-03-06">...</div>
```

```bash
export TARGET_ENDPOINT="https://kontramarka.ua/uk/vesilla-figaro-72721.html"
export TRACK_PATH="div[class*=showDate-]@class"
# extracts: "spoiler showDate-2026-03-05", "spoiler showDate-2026-03-06"
```

**Example — tracking theatre listings on kontramarka.ua:**

```html
<a class="block-info__title" href="...">
  <span itemprop="name">Казки Гофмана</span>
</a>
```

```bash
export TARGET_ENDPOINT="https://kontramarka.ua/uk/theatre/nacionalnaa-opera-18.html?page=0&date=week&genre="
export TRACK_PATH=".block-info__title span[itemprop=name]"
```

Use your browser's DevTools (Inspect Element) to find the right CSS selector for any page.

## REQUEST_HEADERS format

A JSON object where keys are header names and values are header values:

```bash
export REQUEST_HEADERS='{
  "Accept": "application/json",
  "Referer": "https://example.com",
  "User-Agent": "Mozilla/5.0 ..."
}'
```

Omit this variable entirely if no custom headers are needed.

## Running locally

### Build and run once

```bash
export TARGET_ENDPOINT="https://api.example.com/events"
export TELEGRAM_BOT_TOKEN="123456:ABC..."
export TELEGRAM_CHAT_ID="-100123456789"
export TRACK_PATH="events.#.event_date"

go build -o poller main.go
./poller
```

### Schedule with cron (Linux/macOS)

```bash
crontab -e
```

Add a line (runs every 3 hours):

```
0 */3 * * * cd /path/to/state-tracker && ./poller
```

Make sure the environment variables are available to cron. You can source an env file:

```
0 */3 * * * cd /path/to/state-tracker && set -a && . ./.env && set +a && ./poller
```

### Schedule with launchd (macOS)

Create `~/Library/LaunchAgents/com.state-tracker.plist` and load it with `launchctl load`.

## Running on GitHub Actions

This repo includes a workflow at `.github/workflows/poll.yml` that runs on a schedule and commits the state file back to the repo.

### Setup

1. Fork this repository
2. Go to **Settings > Secrets and variables > Actions**
3. Add the required secrets: `TARGET_ENDPOINT`, `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID`, `TRACK_PATH`
4. Optionally add `REQUEST_HEADERS`
5. The workflow will start running on schedule automatically

### Adjust the schedule

Edit `.github/workflows/poll.yml` and change the cron expression:

```yaml
on:
  schedule:
    - cron: '0 */3 * * *'  # every 3 hours
```

Common schedules:

| Cron expression | Frequency |
|---|---|
| `*/30 * * * *` | Every 30 minutes |
| `0 * * * *` | Every hour |
| `0 */3 * * *` | Every 3 hours |
| `0 */6 * * *` | Every 6 hours |
| `0 0 * * *` | Once a day at midnight |

Note: GitHub Actions cron has a minimum interval of 5 minutes and may experience delays of several minutes.

### Run manually

Click **Actions > Poll endpoint > Run workflow** to trigger a run at any time, regardless of the schedule.

### Disable the schedule

Either:

- **Disable the workflow**: Actions > Poll endpoint > three-dot menu > Disable workflow
- **Remove the schedule trigger**: delete the `schedule` block from `poll.yml`, keeping only `workflow_dispatch` for manual runs

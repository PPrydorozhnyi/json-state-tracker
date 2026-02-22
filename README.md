# State Tracker

A lightweight tool that polls a JSON API endpoint, extracts values at a configurable path, and sends a Telegram notification when the set of values changes (new items appear or existing ones disappear).

Works anywhere: run it locally with cron, launchd, Task Scheduler, or any other scheduler — or fork this repo and let GitHub Actions handle it for free.

## How it works

1. Fetches a JSON response from `TARGET_ENDPOINT`
2. Extracts values using a [gjson](https://github.com/tidwall/gjson) path expression (`TRACK_PATH`)
3. Compares the extracted set against the previously saved state
4. Sends a Telegram message listing added/removed values (if any)
5. Saves the new set to `last_response.json`

## Configuration

All configuration is done via environment variables. When running on GitHub Actions, set these as **repository secrets** (Settings > Secrets and variables > Actions).

### Required

| Variable             | Description                                                         |
|----------------------|---------------------------------------------------------------------|
| `TARGET_ENDPOINT`    | Full URL of the JSON API to poll                                    |
| `TELEGRAM_BOT_TOKEN` | Telegram bot token (from [@BotFather](https://t.me/BotFather))      |
| `TELEGRAM_CHAT_ID`   | Telegram chat/group ID to send notifications to                     |
| `TRACK_PATH`         | gjson path expression that extracts the values to track (see below) |

### Optional

| Variable          | Description                                                       |
|-------------------|-------------------------------------------------------------------|
| `REQUEST_HEADERS` | JSON object of HTTP headers to include in the request (see below) |

## TRACK_PATH examples

The `TRACK_PATH` variable uses [gjson syntax](https://github.com/tidwall/gjson/blob/master/SYNTAX.md). The `#` operator iterates arrays.

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

| TRACK_PATH            | Extracted values                         |
|-----------------------|------------------------------------------|
| `events.#.event_date` | `2026-03-15`, `2026-04-01`, `2026-05-20` |
| `events.#.id`         | `1`, `2`, `3`                            |
| `events.#.title`      | `Concert A`, `Concert B`, `Workshop C`   |

For nested structures like `{"data": {"items": [...]}}`, use `data.items.#.field_name`.

See the full [gjson path syntax documentation](https://github.com/tidwall/gjson/blob/master/SYNTAX.md) for advanced queries, filtering, and modifiers.

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

Edit `.github/workflows/poll.yml`, uncomment and change the cron expression:

```yaml
on:
  schedule:
    - cron: '0 */3 * * *'  # every 3 hours
```

Common schedules:

| Cron expression | Frequency              |
|-----------------|------------------------|
| `*/30 * * * *`  | Every 30 minutes       |
| `0 * * * *`     | Every hour             |
| `0 */3 * * *`   | Every 3 hours          |
| `0 */6 * * *`   | Every 6 hours          |
| `0 0 * * *`     | Once a day at midnight |

Note: GitHub Actions cron has a minimum interval of 5 minutes and may experience delays of several minutes.

### Run manually

Click **Actions > Poll endpoint > Run workflow** to trigger a run at any time, regardless of the schedule.

### Disable the schedule

Either:

- **Disable the workflow**: Actions > Poll endpoint > three-dot menu > Disable workflow
- **Remove the schedule trigger**: delete the `schedule` block from `poll.yml`, keeping only `workflow_dispatch` for manual runs

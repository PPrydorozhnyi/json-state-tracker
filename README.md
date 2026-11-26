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

All configuration is done via environment variables. When running on GitHub Actions (Settings > Secrets and variables > Actions), credentials go to **secrets** (`TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID`, `REQUEST_HEADERS`) and everything else to **variables** (`TARGET_ENDPOINT`, `TRACK_PATH`, `TRACK_FIELDS`, `PAGE_MAX`, `PAGE_PARAM`, `PAGE_ITEM_PATH`). Variables stay readable in the UI and are not masked in logs, which helps debugging selectors.

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
| `PAGE_MAX` | Enables pagination when set to an integer `>= 1`; max number of pages to fetch (see [Pagination](#pagination)) |
| `PAGE_PARAM` | Page query parameter name. Default `page` |
| `PAGE_ITEM_PATH` | Unfiltered path matching all items on a page, used only to detect the last page. Default `TRACK_PATH` |
| `TRACK_FIELDS` | HTML only. JSON array of selectors evaluated inside each `TRACK_PATH` match; their values are joined into one record (see [Composite records](#composite-records)) |

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

**Example — only concerts in December on kontramarka.ua (case-insensitive text filter):**

Use the `:matches(regex)` pseudo-class to keep only elements whose text matches a regular expression. `(?i)` makes it case-insensitive, and since the regex is unanchored, any text before or after the word is allowed — so `(?i)концерт` matches "Новорічний концерт", "КОНЦЕРТ до дня Святого Миколая", "Гала-концерт", "Концерту" etc.

```bash
export TARGET_ENDPOINT='https://kontramarka.ua/uk/theatre/nacionalnaa-opera-18.html?page=0&date=01.12.2026%2C31.12.2026&genre='
export TRACK_PATH='.block-info__title span[itemprop=name]:matches((?i)концерт)'
```

Use `:containsOwn("text")` / `:contains("text")` for a plain case-sensitive substring match instead of a regex. Wrap values containing `&` or `(` in single quotes.

**Example — Dynamo Kyiv tickets (exclude archived events):**

Archived listings use `card--archive` on the outer card; upcoming events use the same inner markup without that modifier.

```html
<div class="card card--long">
  <div class="card--content"><a href="...">Upcoming match</a></div>
</div>
<div class="card card--long card--archive">
  <div class="card--content"><a href="...">Past match</a></div>
</div>
```

```bash
export TARGET_ENDPOINT="https://fc-dynamo-kyiv.ticketsbox.com/"
export TRACK_PATH=".card:not(.card--archive) .card--content a"
```

Use your browser's DevTools (Inspect Element) to find the right CSS selector for any page.

## Pagination

Set `PAGE_MAX` to scan a paginated listing page by page. The tool requests `page=0`, `page=1`, ... (the `PAGE_PARAM` query parameter is overwritten if present in `TARGET_ENDPOINT`, added otherwise) and merges `TRACK_PATH` matches from all pages into one set.

Pagination stops when any of these happens:

- the page has no `PAGE_ITEM_PATH` items (past the last page)
- the page has exactly the same items as the previous one (site repeats the last page for out-of-range numbers)
- the request fails (on page 0 this is a fatal error)
- `PAGE_MAX` pages have been fetched

A page with zero `TRACK_PATH` matches does **not** stop pagination. That is why `PAGE_ITEM_PATH` exists: when `TRACK_PATH` filters items (e.g. with `:matches(...)`), set `PAGE_ITEM_PATH` to the same selector without the filter.

**Example — December concerts on opera.com.ua across all pages:**

```bash
export TARGET_ENDPOINT='https://opera.com.ua/afisha?month=01-12-2026'
export TRACK_PATH='.right_part .title a:matches((?i)концерт)'
export PAGE_ITEM_PATH='.right_part .title a'
export PAGE_MAX=20
```

**Example — December concerts on opera.com.ua that have tickets on sale:**

Each event block has a `.bay` div with a "Придбати квиток" link when tickets are available:

```html
<div class="right_part">
  <div class="title"><a href="/afisha/karmen-76">Кармен</a></div>
  <div class="bay"><a href="https://widget.mticket.com.ua/...">Придбати квиток</a></div>
</div>
```

Add `:has(...)` to the concert filter, so a title is kept only when its name matches **and** its block contains that link:

```bash
export TARGET_ENDPOINT='https://opera.com.ua/afisha?month=01-12-2026'
export TRACK_PATH='.right_part:has(.bay a:containsOwn("Придбати квиток")) .title a:matches((?i)концерт)'
export PAGE_ITEM_PATH='.right_part .title a'
export PAGE_MAX=20
```

Use `.right_part:has(.bay a)` instead if any link inside `.bay` is enough, regardless of its text.

**Example — all concerts on kontramarka.ua:**

```bash
export TARGET_ENDPOINT='https://kontramarka.ua/uk/concert?date=&genre='
export TRACK_PATH='.block-info__title span[itemprop=name]'
export PAGE_MAX=30
```

## Composite records

By default each `TRACK_PATH` match gives one value (e.g. an event title). The same show on two dates then collapses into a single record, and a new date is never reported. Build each record from several elements instead.

### HTML: `TRACK_FIELDS`

Point `TRACK_PATH` at the whole item (e.g. the event card) and list the parts to combine in `TRACK_FIELDS` — a JSON array of selectors evaluated **inside each match**. Non-empty values are joined with ` | `.

- Each field supports the `@attr` suffix.
- A field matching several elements contributes all of them, space-separated.
- A field with no match is skipped; an item with no field values is skipped.
- Filter on the container with `:has(...)`, since `TRACK_PATH` now selects the card, not the title.

**Example — opera.com.ua concerts with tickets on sale, one record per performance:**

```html
<div class="item">
  <div class="left_part">
    <div class="date">19/12</div>
    <div class="row row_date">Початок: <b>15:00</b><br />Завершення: <b>17:30</b></div>
  </div>
  <div class="right_part">
    <div class="title"><a href="...">Новорічно-Різдвяний концерт</a></div>
    <div class="bay"><a href="...">Придбати квиток</a></div>
  </div>
</div>
```

```bash
export TARGET_ENDPOINT='https://opera.com.ua/afisha?month=01-12-2026'
export TRACK_PATH='.item:has(.bay a):has(.title a:matches((?i)концерт))'
export TRACK_FIELDS='[".date", ".row_date b:first-of-type", ".title a"]'
export PAGE_ITEM_PATH='.right_part .title a'
export PAGE_MAX=20
# extracts:
#   "06/12 | 15:00 | Концерт до Дня Святого Миколая"
#   "19/12 | 15:00 | Новорічно-Різдвяний концерт"
#   "20/12 | 17:00 | Новорічно-Різдвяний концерт"
```

The start time is included because the same date can repeat too (e.g. two performances on one day).

### JSON: gjson multipaths

No extra setting needed — a gjson [multipath](https://github.com/tidwall/gjson/blob/master/SYNTAX.md#multipaths) builds a composite value per array element.

```json
{
  "events": [
    { "id": 1, "event_date": "2026-12-19", "title": "New Year Gala" },
    { "id": 2, "event_date": "2026-12-20", "title": "New Year Gala" }
  ]
}
```

| TRACK_PATH | Extracted values |
|---|---|
| `events.#.title` | `New Year Gala` (one record, duplicates collapse) |
| `events.#.{event_date,title}` | `{"event_date":"2026-12-19","title":"New Year Gala"}`, `{"event_date":"2026-12-20","title":"New Year Gala"}` |
| `events.#.{date:event_date,name:title}` | `{"date":"2026-12-19","name":"New Year Gala"}`, `{"date":"2026-12-20","name":"New Year Gala"}` |

**Note:** switching an existing tracker to composite records changes the value format, so the first run reports every old value as removed and every new one as added. Delete `last_response.json` first to avoid that one noisy message.

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
3. **Secrets** tab: add `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID`, optionally `REQUEST_HEADERS`
4. **Variables** tab: add `TARGET_ENDPOINT`, `TRACK_PATH`, optionally `TRACK_FIELDS`, `PAGE_MAX`, `PAGE_PARAM`, `PAGE_ITEM_PATH`
5. The workflow will start running on schedule automatically

### Adjust the schedule

Edit `.github/workflows/poll.yml` and change the cron expression:

```yaml
on:
  schedule:
    - cron: '17 */4 * * *'  # every 4 hours, at minute 17
```

Prefer an off-minute (like `17`) over `0`: top-of-hour schedules are GitHub's busiest slot and get delayed or dropped.

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

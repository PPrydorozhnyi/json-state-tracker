// Command state-tracker polls a JSON API or HTML page, extracts tracked values
// and sends a Telegram notification when the set of values changes.
package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"maps"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/PuerkitoBio/goquery"
	"github.com/tidwall/gjson"
)

// pageDelay is a var only so tests can disable it.
var pageDelay = 500 * time.Millisecond

const (
	maxMessageRunes   = 4000
	maxErrorBodyRunes = 200
	fieldSeparator    = " | "
)

// httpClient is shared so paginated requests reuse keep-alive connections.
var httpClient = &http.Client{Timeout: 10 * time.Second}

func main() {
	// Cancelled on Ctrl+C / SIGTERM so in-flight requests abort promptly.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	endpoint := mustEnv("TARGET_ENDPOINT")
	tkn := mustEnv("TELEGRAM_BOT_TOKEN")
	chatID := mustEnv("TELEGRAM_CHAT_ID")
	trackPath := mustEnv("TRACK_PATH") // gjson path for JSON, CSS selector for HTML
	stateFile := "last_response.json"

	notify := func(msg string) {
		if err := sendTelegram(ctx, tkn, chatID, msg); err != nil {
			log.Printf("telegram notify failed: %v", err)
		}
	}
	fatal := func(what string, err error) {
		notify(fmt.Sprintf("%s: %v", what, err))
		stop()
		log.Fatalf("%s: %v", what, err)
	}

	headers, err := parseHeaders(os.Getenv("REQUEST_HEADERS"))
	if err != nil {
		fatal("bad headers config", err)
	}

	maxPages, isPaginated, err := parsePageMax(os.Getenv("PAGE_MAX"))
	if err != nil {
		fatal("bad pagination config", err)
	}

	fields, err := parseTrackFields(os.Getenv("TRACK_FIELDS"))
	if err != nil {
		fatal("bad track fields config", err)
	}

	var newSet map[string]bool
	isPartial := false
	if isPaginated {
		pageParam := cmp.Or(os.Getenv("PAGE_PARAM"), "page")
		itemPath := cmp.Or(os.Getenv("PAGE_ITEM_PATH"), trackPath)
		newSet, isPartial, err = fetchAllPages(ctx, endpoint, headers, trackPath, fields, itemPath, pageParam, maxPages)
	} else {
		newSet, err = fetchSet(ctx, endpoint, headers, trackPath, fields)
	}
	if err != nil {
		fatal("fetch failed", err)
	}
	// A partial scan would report items from unfetched pages as removed.
	if isPartial {
		msg := fmt.Sprintf("Partial scan of %s, state not updated.", endpoint)
		log.Println(msg)
		notify(msg)
		return
	}
	if len(newSet) == 0 {
		log.Printf("warning: TRACK_PATH %q matched 0 values", trackPath)
	}

	oldSet, err := loadSet(stateFile)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		log.Println("First run, saving baseline.")
		notify("First run, saving baseline.")
	case err != nil:
		fatal("load state failed", err)
	default:
		added, removed := diffSets(oldSet, newSet)
		if len(added) > 0 || len(removed) > 0 {
			msg := formatChanges(endpoint, added, removed)
			notify(msg)
			log.Println("Change detected, notification sent.")
		} else {
			log.Println("No change.")
		}
	}

	if err := saveSet(stateFile, newSet); err != nil {
		fatal("save state failed", err)
	}
}

func fetchSet(ctx context.Context, endpoint string, headers map[string]string, trackPath string, fields []string) (map[string]bool, error) {
	doc, err := fetchDocument(ctx, endpoint, headers)
	if err != nil {
		return nil, err
	}
	return toSet(doc.extract(trackPath, fields)), nil
}

// fetchAllPages walks pages 0..maxPages-1 sequentially and merges TRACK_PATH
// matches from every page. The end of pagination is decided by itemPath
// (unfiltered items), because TRACK_PATH may filter out everything on a page
// that is not the last one.
//
// isPartial is true when a page after the first one failed to fetch, so the
// merged set is missing values from the remaining pages.
func fetchAllPages(ctx context.Context, endpoint string, headers map[string]string, trackPath string, fields []string, itemPath, pageParam string, maxPages int) (merged map[string]bool, isPartial bool, err error) {
	merged = map[string]bool{}
	var prevItems []string

	for page := range maxPages {
		if page > 0 {
			time.Sleep(pageDelay)
		}
		pageURL, err := withPage(endpoint, pageParam, page)
		if err != nil {
			return nil, false, err
		}

		doc, err := fetchDocument(ctx, pageURL, headers)
		if err != nil {
			if page == 0 {
				return nil, false, err
			}
			log.Printf("page %d: fetch failed, stopping pagination: %v", page, err)
			return merged, true, nil
		}

		items := doc.extract(itemPath, nil)
		if len(items) == 0 {
			log.Printf("page %d: no items, end of pagination", page)
			break
		}
		// Sites often serve the last page again for out-of-range numbers.
		if slices.Equal(items, prevItems) {
			log.Printf("page %d: same items as previous page, end of pagination", page)
			break
		}
		prevItems = items

		matches := items
		if trackPath != itemPath || len(fields) > 0 {
			matches = doc.extract(trackPath, fields)
		}
		log.Printf("page %d: %d items, %d matches", page, len(items), len(matches))
		for _, v := range matches {
			merged[v] = true
		}

		if page == maxPages-1 {
			log.Printf("page %d: PAGE_MAX=%d reached, stopping pagination", page, maxPages)
		}
	}
	return merged, false, nil
}

// withPage sets (or adds) the page query parameter on the endpoint URL.
func withPage(endpoint, pageParam string, page int) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("parse endpoint: %w", err)
	}
	q := u.Query()
	q.Set(pageParam, strconv.Itoa(page))
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// parsePageMax enables pagination only when PAGE_MAX is a positive integer.
func parsePageMax(raw string) (maxPages int, isEnabled bool, err error) {
	if raw == "" {
		return 0, false, nil
	}
	maxPages, err = strconv.Atoi(raw)
	if err != nil || maxPages < 1 {
		return 0, false, fmt.Errorf("PAGE_MAX must be an integer >= 1, got %q", raw)
	}
	return maxPages, true, nil
}

// parseTrackFields reads TRACK_FIELDS as a JSON array of selectors. JSON is
// used instead of a comma list because commas are valid CSS selector syntax.
func parseTrackFields(raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	var fields []string
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return nil, fmt.Errorf("invalid TRACK_FIELDS JSON array: %w", err)
	}
	return fields, nil
}

// document is a fetched response parsed once, so several paths can be
// extracted from it without re-parsing.
type document struct {
	html *goquery.Document // nil for JSON responses
	json []byte
}

func fetchDocument(ctx context.Context, endpoint string, headers map[string]string) (*document, error) {
	body, contentType, err := executeFetch(ctx, endpoint, headers)
	if err != nil {
		return nil, err
	}
	if detectFormat(contentType) != "html" {
		return &document{json: body}, nil
	}
	html, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parse HTML: %w", err)
	}
	return &document{html: html}, nil
}

// extract returns values for path. fields apply to HTML only; JSON builds
// composite values with gjson multipaths instead.
func (d *document) extract(path string, fields []string) []string {
	if d.html != nil {
		return extractHTML(d.html, path, fields)
	}
	return extractJSON(d.json, path)
}

func toSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, v := range values {
		set[v] = true
	}
	return set
}

func extractJSON(data []byte, path string) []string {
	result := gjson.GetBytes(data, path)
	arr := result.Array()
	values := make([]string, 0, len(arr))
	for _, v := range arr {
		values = append(values, v.String())
	}
	return values
}

// extractHTML finds all elements matching the CSS selector and collects their
// values in document order.
//
// TRACK_PATH can optionally end with @attr to extract an attribute value
// instead of text content. For example:
//
//	".title a"                     → text content of each matched element
//	"div[class*=showDate-]@class"  → class attribute of each matched element
//
// When fields are given, each matched element becomes one composite record
// built from those sub-selectors (see composeRecord).
//
// Empty values are silently skipped.
func extractHTML(doc *goquery.Document, path string, fields []string) []string {
	selector, attr := parseHTMLPath(path)

	var values []string
	doc.Find(selector).Each(func(_ int, s *goquery.Selection) {
		var val string
		if len(fields) > 0 {
			val = composeRecord(s, fields)
		} else {
			val = selectionValue(s, attr)
		}
		if val != "" {
			values = append(values, val)
		}
	})
	return values
}

// composeRecord evaluates each field selector relative to container and joins
// the non-empty results with fieldSeparator. A field matching several
// elements contributes all of their values, space-separated.
func composeRecord(container *goquery.Selection, fields []string) string {
	var parts []string
	for _, field := range fields {
		selector, attr := parseHTMLPath(field)
		var fieldValues []string
		container.Find(selector).Each(func(_ int, s *goquery.Selection) {
			if val := selectionValue(s, attr); val != "" {
				fieldValues = append(fieldValues, val)
			}
		})
		if len(fieldValues) > 0 {
			parts = append(parts, strings.Join(fieldValues, " "))
		}
	}
	return strings.Join(parts, fieldSeparator)
}

// selectionValue returns the trimmed attribute value, or text when attr is empty.
func selectionValue(s *goquery.Selection, attr string) string {
	if attr == "" {
		return strings.TrimSpace(s.Text())
	}
	val, _ := s.Attr(attr)
	return strings.TrimSpace(val)
}

// parseHTMLPath splits a TRACK_PATH like "div.foo@class" into the CSS
// selector ("div.foo") and the attribute name ("class"). If there is no
// @attr suffix, attr is empty and the full path is the selector.
func parseHTMLPath(path string) (selector, attr string) {
	if selector, attr, found := strings.CutLast(path, "@"); found {
		return selector, attr
	}
	return path, ""
}

// detectFormat determines whether to parse the response as JSON or HTML
// based on the Content-Type header returned by the server.
func detectFormat(contentType string) string {
	if strings.Contains(contentType, "text/html") {
		return "html"
	}
	return "json"
}

func loadSet(path string) (map[string]bool, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	var values []string
	if err := json.Unmarshal(data, &values); err != nil {
		return nil, fmt.Errorf("parse state file: %w", err)
	}
	return toSet(values), nil
}

func saveSet(path string, set map[string]bool) error {
	values := slices.Sorted(maps.Keys(set))
	// Keep writing [] (not null) for an empty set.
	if values == nil {
		values = []string{}
	}
	data, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600) //nolint:gosec // G703: path is the constant state file name
}

func diffSets(oldSet, newSet map[string]bool) (added, removed []string) {
	for v := range newSet {
		if !oldSet[v] {
			added = append(added, v)
		}
	}
	for v := range oldSet {
		if !newSet[v] {
			removed = append(removed, v)
		}
	}
	slices.Sort(added)
	slices.Sort(removed)
	return
}

func formatChanges(endpoint string, added, removed []string) string {
	var buf strings.Builder
	fmt.Fprintf(&buf, "Endpoint state changed: %s\n", endpoint)

	if len(added) > 0 {
		fmt.Fprintf(&buf, "\nAdded (%d):\n", len(added))
		for _, v := range added {
			fmt.Fprintf(&buf, "  %s\n", v)
		}
	}
	if len(removed) > 0 {
		fmt.Fprintf(&buf, "\nRemoved (%d):\n", len(removed))
		for _, v := range removed {
			fmt.Fprintf(&buf, "  %s\n", v)
		}
	}

	msg := buf.String()
	if truncated := truncateRunes(msg, maxMessageRunes); truncated != msg {
		return truncated + "\n... (truncated)"
	}
	return msg
}

// truncateRunes cuts s to at most n runes, never splitting a multi-byte
// character (Telegram rejects invalid UTF-8).
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func mustEnv(name string) string {
	val := os.Getenv(name)
	if val == "" {
		log.Fatalf("required environment variable %s is not set", name)
	}
	return val
}

func parseHeaders(raw string) (map[string]string, error) {
	if raw == "" {
		return nil, nil
	}
	var headers map[string]string
	if err := json.Unmarshal([]byte(raw), &headers); err != nil {
		return nil, fmt.Errorf("invalid REQUEST_HEADERS JSON: %w", err)
	}
	return headers, nil
}

func executeFetch(ctx context.Context, endpoint string, headers map[string]string) (body []byte, contentType string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return nil, "", fmt.Errorf("build request: %w", err)
	}

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncateRunes(string(body), maxErrorBodyRunes))
	}
	return body, resp.Header.Get("Content-Type"), nil
}

func sendTelegram(ctx context.Context, tkn, chatID, message string) error {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", tkn)
	form := url.Values{
		"chat_id": {chatID},
		"text":    {message},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("build telegram request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("telegram request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telegram HTTP %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

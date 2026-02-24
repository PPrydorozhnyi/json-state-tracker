package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/tidwall/gjson"
)

func main() {
	endpoint := mustEnv("TARGET_ENDPOINT")
	tkn := mustEnv("TELEGRAM_BOT_TOKEN")
	chatID := mustEnv("TELEGRAM_CHAT_ID")
	trackPath := mustEnv("TRACK_PATH") // gjson path for JSON, CSS selector for HTML
	stateFile := "last_response.json"

	notify := func(msg string) {
		if err := sendTelegram(tkn, chatID, msg); err != nil {
			log.Printf("telegram notify failed: %v", err)
		}
	}
	fatal := func(context string, err error) {
		notify(fmt.Sprintf("%s: %v", context, err))
		log.Fatalf("%s: %v", context, err)
	}

	headers, err := parseHeaders(os.Getenv("REQUEST_HEADERS"))
	if err != nil {
		fatal("bad headers config", err)
	}

	body, contentType, err := executeFetch(endpoint, headers)
	if err != nil {
		fatal("fetch failed", err)
	}

	format := detectFormat(contentType)

	var newSet map[string]bool
	switch format {
	case "html":
		newSet, err = extractSetHTML(body, trackPath)
		if err != nil {
			fatal("html extraction failed", err)
		}
	default:
		newSet = extractSetJSON(body, trackPath)
	}
	if len(newSet) == 0 {
		log.Printf("warning: TRACK_PATH %q matched 0 values", trackPath)
	}

	oldSet, err := loadSet(stateFile)
	if err != nil {
		log.Println("First run, saving baseline.")
		notify("First run, saving baseline.")
	} else {
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
		log.Fatalf("save state: %v", err)
	}
}

func extractSetJSON(data []byte, path string) map[string]bool {
	result := gjson.GetBytes(data, path)
	set := map[string]bool{}
	for _, v := range result.Array() {
		set[v.String()] = true
	}
	return set
}

// extractSetHTML parses the response body as HTML, finds all elements matching
// the CSS selector, and collects a unique set of values from them.
//
// TRACK_PATH can optionally end with @attr to extract an attribute value
// instead of text content. For example:
//
//	".title a"                     → text content of each matched element
//	"div[class*=showDate-]@class"  → class attribute of each matched element
//
// Empty values and duplicates are silently skipped.
func extractSetHTML(data []byte, path string) (map[string]bool, error) {
	selector, attr := parseHTMLPath(path)

	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("parse HTML: %w", err)
	}

	set := map[string]bool{}
	doc.Find(selector).Each(func(_ int, s *goquery.Selection) {
		var val string
		if attr != "" {
			val, _ = s.Attr(attr)
		} else {
			val = s.Text()
		}
		if val = strings.TrimSpace(val); val != "" {
			set[val] = true
		}
	})
	return set, nil
}

// parseHTMLPath splits a TRACK_PATH like "div.foo@class" into the CSS
// selector ("div.foo") and the attribute name ("class"). If there is no
// @attr suffix, attr is empty and the full path is the selector.
func parseHTMLPath(path string) (selector, attr string) {
	if i := strings.LastIndex(path, "@"); i != -1 {
		return path[:i], path[i+1:]
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
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var values []string
	if err := json.Unmarshal(data, &values); err != nil {
		return nil, fmt.Errorf("parse state file: %w", err)
	}
	set := make(map[string]bool, len(values))
	for _, v := range values {
		set[v] = true
	}
	return set, nil
}

func saveSet(path string, set map[string]bool) error {
	values := make([]string, 0, len(set))
	for v := range set {
		values = append(values, v)
	}
	sort.Strings(values)
	data, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
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
	sort.Strings(added)
	sort.Strings(removed)
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
	if len(msg) > 4000 {
		return msg[:4000] + "\n... (truncated)"
	}
	return msg
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

func executeFetch(endpoint string, headers map[string]string) (body []byte, contentType string, err error) {
	client := &http.Client{Timeout: 10 * time.Second}

	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return nil, "", fmt.Errorf("build request: %w", err)
	}

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	body, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
	return body, resp.Header.Get("Content-Type"), nil
}

func sendTelegram(tkn, chatID, message string) error {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", tkn)
	resp, err := http.PostForm(apiURL, url.Values{
		"chat_id": {chatID},
		"text":    {message},
	})
	if err != nil {
		return fmt.Errorf("telegram request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telegram HTTP %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

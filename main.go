package main

import (
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

	"github.com/tidwall/gjson"
)

func main() {
	endpoint := mustEnv("TARGET_ENDPOINT")
	tkn := mustEnv("TELEGRAM_BOT_TOKEN")
	chatID := mustEnv("TELEGRAM_CHAT_ID")
	trackPath := mustEnv("TRACK_PATH")
	stateFile := "last_response.json"

	headers, err := parseHeaders(os.Getenv("REQUEST_HEADERS"))
	if err != nil {
		sendTelegram(tkn, chatID, fmt.Sprintf("Bad headers config: %v", err))
		log.Fatalf("bad headers config: %v", err)
	}

	body, err := executeFetch(endpoint, headers)
	if err != nil {
		sendTelegram(tkn, chatID, "Failed to fetch latest response")
		log.Fatalf("Failed to fetch latest response: %v", err)
	}

	newSet := extractSet(body, trackPath)
	if len(newSet) == 0 {
		log.Printf("warning: TRACK_PATH %q matched 0 values", trackPath)
	}

	oldSet, err := loadSet(stateFile)
	if err != nil {
		log.Println("First run, saving baseline.")
		sendTelegram(tkn, chatID, "First run, saving baseline.")
	} else {
		added, removed := diffSets(oldSet, newSet)
		if len(added) > 0 || len(removed) > 0 {
			msg := formatChanges(endpoint, added, removed)
			sendTelegram(tkn, chatID, msg)
			log.Println("Change detected, notification sent.")
		} else {
			log.Println("No change.")
		}
	}

	if err := saveSet(stateFile, newSet); err != nil {
		log.Fatalf("write failed: %v\n", err)
	}
}

func extractSet(data []byte, path string) map[string]bool {
	result := gjson.GetBytes(data, path)
	set := map[string]bool{}
	for _, v := range result.Array() {
		set[v.String()] = true
	}
	return set
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

func executeFetch(endpoint string, headers map[string]string) ([]byte, error) {
	client := &http.Client{Timeout: 5 * time.Second}

	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		log.Printf("fetch failed: %v\n", err)
		return nil, err
	}
	defer resp.Body.Close()

	newData, err := io.ReadAll(resp.Body)
	return newData, err
}

func sendTelegram(tkn, chatID, message string) {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", tkn)
	resp, err := http.PostForm(apiURL, url.Values{
		"chat_id": {chatID},
		"text":    {message},
	})
	if err != nil {
		log.Printf("telegram send failed: %v\n", err)
		return
	}
	defer resp.Body.Close()
}

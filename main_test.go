package main

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"github.com/PuerkitoBio/goquery"
)

// --- config parsing ---

func TestParsePageMax(t *testing.T) {
	tests := []struct {
		raw         string
		wantMax     int
		wantEnabled bool
		wantErr     bool
	}{
		{raw: "", wantMax: 0, wantEnabled: false},
		{raw: "1", wantMax: 1, wantEnabled: true},
		{raw: "20", wantMax: 20, wantEnabled: true},
		{raw: "0", wantErr: true},
		{raw: "-1", wantErr: true},
		{raw: "abc", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			gotMax, gotEnabled, err := parsePageMax(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if gotMax != tt.wantMax || gotEnabled != tt.wantEnabled {
				t.Errorf("got (%d, %v), want (%d, %v)", gotMax, gotEnabled, tt.wantMax, tt.wantEnabled)
			}
		})
	}
}

func TestParseTrackFields(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    []string
		wantErr bool
	}{
		{name: "empty", raw: "", want: nil},
		{name: "valid", raw: `[".date", ".title a@href"]`, want: []string{".date", ".title a@href"}},
		{name: "not an array", raw: `.date`, wantErr: true},
		{name: "object", raw: `{"a":"b"}`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseTrackFields(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseHeaders(t *testing.T) {
	got, err := parseHeaders("")
	if err != nil || got != nil {
		t.Errorf("empty: got (%v, %v), want (nil, nil)", got, err)
	}

	got, err = parseHeaders(`{"Accept":"application/json","User-Agent":"x"}`)
	if err != nil {
		t.Fatalf("valid: unexpected error %v", err)
	}
	if got["Accept"] != "application/json" || got["User-Agent"] != "x" {
		t.Errorf("valid: got %v", got)
	}

	if _, err := parseHeaders(`not json`); err == nil {
		t.Error("invalid: expected error")
	}
}

func TestWithPage(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		param    string
		page     int
		want     url.Values
	}{
		{
			name:     "adds missing param",
			endpoint: "https://opera.com.ua/afisha?month=01-12-2026",
			param:    "page",
			page:     0,
			want:     url.Values{"month": {"01-12-2026"}, "page": {"0"}},
		},
		{
			name:     "overwrites existing param",
			endpoint: "https://kontramarka.ua/uk/concert?page=7&date=&genre=",
			param:    "page",
			page:     2,
			want:     url.Values{"page": {"2"}, "date": {""}, "genre": {""}},
		},
		{
			name:     "custom param name",
			endpoint: "https://example.com/list",
			param:    "p",
			page:     5,
			want:     url.Values{"p": {"5"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := withPage(tt.endpoint, tt.param, tt.page)
			if err != nil {
				t.Fatalf("unexpected error %v", err)
			}
			u, err := url.Parse(got)
			if err != nil {
				t.Fatalf("result not a URL: %v", err)
			}
			if gotQuery := u.Query(); fmt.Sprint(gotQuery) != fmt.Sprint(tt.want) {
				t.Errorf("query = %v, want %v", gotQuery, tt.want)
			}
		})
	}

	if _, err := withPage("://bad", "page", 0); err == nil {
		t.Error("bad URL: expected error")
	}
}

func TestParseHTMLPath(t *testing.T) {
	tests := []struct {
		path         string
		wantSelector string
		wantAttr     string
	}{
		{path: ".title a", wantSelector: ".title a", wantAttr: ""},
		{path: "div[class*=showDate-]@class", wantSelector: "div[class*=showDate-]", wantAttr: "class"},
		{path: `a[href*="@"]@href`, wantSelector: `a[href*="@"]`, wantAttr: "href"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			gotSelector, gotAttr := parseHTMLPath(tt.path)
			if gotSelector != tt.wantSelector || gotAttr != tt.wantAttr {
				t.Errorf("got (%q, %q), want (%q, %q)", gotSelector, gotAttr, tt.wantSelector, tt.wantAttr)
			}
		})
	}
}

func TestDetectFormat(t *testing.T) {
	tests := map[string]string{
		"text/html; charset=utf-8": "html",
		"text/html":                "html",
		"application/json":         "json",
		"":                         "json",
	}
	for contentType, want := range tests {
		if got := detectFormat(contentType); got != want {
			t.Errorf("detectFormat(%q) = %q, want %q", contentType, got, want)
		}
	}
}

// --- message formatting ---

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("short", 10); got != "short" {
		t.Errorf("short: got %q", got)
	}

	got := truncateRunes("Концерт", 3)
	if got != "Кон" {
		t.Errorf("cyrillic: got %q, want %q", got, "Кон")
	}
	if !utf8.ValidString(got) {
		t.Error("cyrillic: result is not valid UTF-8")
	}
}

func TestDiffSets(t *testing.T) {
	oldSet := toSet([]string{"a", "b", "c"})
	newSet := toSet([]string{"b", "d", "c", "e"})

	added, removed := diffSets(oldSet, newSet)
	if want := []string{"d", "e"}; !slices.Equal(added, want) {
		t.Errorf("added = %q, want %q", added, want)
	}
	if want := []string{"a"}; !slices.Equal(removed, want) {
		t.Errorf("removed = %q, want %q", removed, want)
	}

	added, removed = diffSets(oldSet, oldSet)
	if len(added) != 0 || len(removed) != 0 {
		t.Errorf("no change: got added %q, removed %q", added, removed)
	}
}

func TestFormatChanges(t *testing.T) {
	msg := formatChanges("https://example.com", []string{"new"}, []string{"gone"})
	for _, want := range []string{"https://example.com", "Added (1):", "  new", "Removed (1):", "  gone"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q:\n%s", want, msg)
		}
	}

	onlyAdded := formatChanges("e", []string{"x"}, nil)
	if strings.Contains(onlyAdded, "Removed") {
		t.Errorf("unexpected Removed section:\n%s", onlyAdded)
	}
}

func TestFormatChangesTruncatesOnRuneBoundary(t *testing.T) {
	var added []string
	for i := range 500 {
		added = append(added, fmt.Sprintf("Новорічно-Різдвяний концерт %d", i))
	}

	msg := formatChanges("e", added, nil)

	if !strings.HasSuffix(msg, "\n... (truncated)") {
		t.Error("expected truncation suffix")
	}
	if !utf8.ValidString(msg) {
		t.Error("truncated message is not valid UTF-8")
	}
	if got, limit := utf8.RuneCountInString(msg), maxMessageRunes+len("\n... (truncated)"); got > limit {
		t.Errorf("message has %d runes, want <= %d", got, limit)
	}
}

// --- state file ---

func TestSaveLoadSetRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	set := toSet([]string{"Кармен", "Баядерка", "Дон Кіхот"})

	if err := saveSet(path, set); err != nil {
		t.Fatalf("saveSet: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "[\n  \"Баядерка\",\n  \"Дон Кіхот\",\n  \"Кармен\"\n]"
	if string(data) != want {
		t.Errorf("file content = %s, want sorted %s", data, want)
	}

	loaded, err := loadSet(path)
	if err != nil {
		t.Fatalf("loadSet: %v", err)
	}
	if added, removed := diffSets(set, loaded); len(added)+len(removed) != 0 {
		t.Errorf("round trip changed set: added %q, removed %q", added, removed)
	}
}

func TestSaveSetEmptyWritesArray(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := saveSet(path, map[string]bool{}); err != nil {
		t.Fatalf("saveSet: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "[]" {
		t.Errorf("file content = %q, want %q", data, "[]")
	}
}

func TestLoadSetErrors(t *testing.T) {
	dir := t.TempDir()

	_, err := loadSet(filepath.Join(dir, "missing.json"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing file: err = %v, want fs.ErrNotExist", err)
	}

	corrupt := filepath.Join(dir, "corrupt.json")
	if err := os.WriteFile(corrupt, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = loadSet(corrupt)
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		t.Errorf("corrupt file: err = %v, want parse error", err)
	}
}

// --- extraction ---

type eventCard struct {
	date, startTime, title string
	isOnSale               bool
}

// operaCard renders markup modeled on an opera.com.ua afisha event card.
func operaCard(c eventCard) string {
	bay := ""
	if c.isOnSale {
		bay = `<div class="bay"><a href="https://widget.example/1">Придбати квиток</a></div>`
	}
	return fmt.Sprintf(`<div class="item">
  <div class="left_part">
    <div class="date">%s</div>
    <div class="row row_date">Початок: <b>%s</b><br />Завершення: <b>23:00</b></div>
  </div>
  <div class="right_part">
    <div class="title"><a href="/afisha/%s">%s</a></div>
    %s
  </div>
</div>`, c.date, c.startTime, c.title, c.title, bay)
}

func operaPage(cards ...eventCard) string {
	var b strings.Builder
	b.WriteString("<html><body>")
	for _, c := range cards {
		b.WriteString(operaCard(c))
	}
	b.WriteString("</body></html>")
	return b.String()
}

func parseHTML(t *testing.T, html string) *goquery.Document {
	t.Helper()
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	return doc
}

var decemberCards = []eventCard{
	{date: "06/12", startTime: "15:00", title: "Концерт до Дня Святого Миколая", isOnSale: true},
	{date: "11/12", startTime: "18:00", title: "Чарівна флейта", isOnSale: true},
	{date: "19/12", startTime: "15:00", title: "Новорічно-Різдвяний концерт", isOnSale: true},
	{date: "20/12", startTime: "17:00", title: "Новорічно-Різдвяний концерт", isOnSale: true},
	{date: "27/12", startTime: "12:00", title: "Різдвяний концерт", isOnSale: false},
}

func TestExtractHTML(t *testing.T) {
	doc := parseHTML(t, operaPage(decemberCards...))

	tests := []struct {
		name string
		path string
		want []string
	}{
		{
			name: "text keeps document order and duplicates",
			path: ".right_part .title a",
			want: []string{"Концерт до Дня Святого Миколая", "Чарівна флейта", "Новорічно-Різдвяний концерт", "Новорічно-Різдвяний концерт", "Різдвяний концерт"},
		},
		{
			name: "attribute",
			path: ".bay a@href",
			want: []string{"https://widget.example/1", "https://widget.example/1", "https://widget.example/1", "https://widget.example/1"},
		},
		{
			name: "name filter and on-sale filter",
			path: `.right_part:has(.bay a:containsOwn("Придбати квиток")) .title a:matches((?i)концерт)`,
			want: []string{"Концерт до Дня Святого Миколая", "Новорічно-Різдвяний концерт", "Новорічно-Різдвяний концерт"},
		},
		{
			name: "no match",
			path: ".missing",
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractHTML(doc, tt.path, nil); !slices.Equal(got, tt.want) {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractHTMLSkipsEmptyValues(t *testing.T) {
	doc := parseHTML(t, `<a class="t">  </a><a class="t" href="">x</a><a class="t" href="/y">y</a>`)

	if got, want := extractHTML(doc, "a.t", nil), []string{"x", "y"}; !slices.Equal(got, want) {
		t.Errorf("text: got %q, want %q", got, want)
	}
	if got, want := extractHTML(doc, "a.t@href", nil), []string{"/y"}; !slices.Equal(got, want) {
		t.Errorf("attr: got %q, want %q", got, want)
	}
}

func TestExtractHTMLWithFields(t *testing.T) {
	doc := parseHTML(t, operaPage(decemberCards...))
	fields := []string{".date", ".row_date b:first-of-type", ".title a"}

	got := extractHTML(doc, ".item:has(.bay a):has(.title a:matches((?i)концерт))", fields)

	want := []string{
		"06/12 | 15:00 | Концерт до Дня Святого Миколая",
		"19/12 | 15:00 | Новорічно-Різдвяний концерт",
		"20/12 | 17:00 | Новорічно-Різдвяний концерт",
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if len(toSet(got)) != 3 {
		t.Error("repeated title on two dates must stay two distinct records")
	}
}

func TestComposeRecord(t *testing.T) {
	doc := parseHTML(t, `<div class="card">
  <span class="date">19/12</span>
  <span class="tag">gala</span><span class="tag">new year</span>
  <a class="title" href="/gala">Gala</a>
</div>`)
	card := doc.Find(".card")

	tests := []struct {
		name   string
		fields []string
		want   string
	}{
		{name: "joins fields", fields: []string{".date", ".title"}, want: "19/12 | Gala"},
		{name: "multi-element field space-joined", fields: []string{".tag"}, want: "gala new year"},
		{name: "attribute field", fields: []string{".title@href"}, want: "/gala"},
		{name: "missing field skipped", fields: []string{".date", ".missing", ".title"}, want: "19/12 | Gala"},
		{name: "all fields missing", fields: []string{".missing"}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := composeRecord(card, tt.fields); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractHTMLWithFieldsSkipsEmptyRecords(t *testing.T) {
	doc := parseHTML(t, `<div class="card"><span class="date">1</span></div><div class="card"></div>`)

	if got, want := extractHTML(doc, ".card", []string{".date"}), []string{"1"}; !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExtractJSON(t *testing.T) {
	data := []byte(`{"events":[
		{"id":1,"event_date":"2026-12-19","title":"New Year Gala"},
		{"id":2,"event_date":"2026-12-20","title":"New Year Gala"}
	]}`)

	tests := []struct {
		path string
		want []string
	}{
		{path: "events.#.title", want: []string{"New Year Gala", "New Year Gala"}},
		{path: "events.#.id", want: []string{"1", "2"}},
		{
			path: "events.#.{event_date,title}",
			want: []string{
				`{"event_date":"2026-12-19","title":"New Year Gala"}`,
				`{"event_date":"2026-12-20","title":"New Year Gala"}`,
			},
		},
		{path: "missing.#.x", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := extractJSON(data, tt.path); !slices.Equal(got, tt.want) {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// --- HTTP ---

func TestExecuteFetchSendsHeaders(t *testing.T) {
	var gotAgent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAgent = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	body, contentType, err := executeFetch(srv.URL, map[string]string{"User-Agent": "test-agent"})
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	if gotAgent != "test-agent" {
		t.Errorf("server got User-Agent %q", gotAgent)
	}
	if string(body) != "{}" || contentType != "application/json" {
		t.Errorf("got body %q, content type %q", body, contentType)
	}
}

func TestExecuteFetchTruncatesErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(strings.Repeat("x", 5000)))
	}))
	t.Cleanup(srv.Close)

	_, _, err := executeFetch(srv.URL, nil)
	if err == nil {
		t.Fatal("expected error for HTTP 500")
	}
	if !strings.HasPrefix(err.Error(), "HTTP 500: ") {
		t.Errorf("error = %q, want HTTP 500 prefix", err)
	}
	if got, limit := len(err.Error()), len("HTTP 500: ")+maxErrorBodyRunes; got > limit {
		t.Errorf("error length %d, want <= %d", got, limit)
	}
}

func TestFetchDocumentDetectsFormat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/html" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<a class="t">Кармен</a>`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":["Кармен"]}`))
	}))
	t.Cleanup(srv.Close)

	htmlDoc, err := fetchDocument(srv.URL+"/html", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := htmlDoc.extract("a.t", nil); !slices.Equal(got, []string{"Кармен"}) {
		t.Errorf("html: got %q", got)
	}

	jsonDoc, err := fetchDocument(srv.URL+"/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if jsonDoc.html != nil {
		t.Error("json: expected no HTML document")
	}
	if got := jsonDoc.extract("items", nil); !slices.Equal(got, []string{"Кармен"}) {
		t.Errorf("json: got %q", got)
	}
}

// pagedServer serves pageBody(page) as HTML for ?page=N and counts requests.
// pageBody returns ok=false to answer with HTTP 500.
func pagedServer(t *testing.T, pageBody func(page int) (html string, isOK bool)) (srv *httptest.Server, requests *atomic.Int32) {
	t.Helper()
	requests = &atomic.Int32{}
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		page, err := strconv.Atoi(r.URL.Query().Get("page"))
		if err != nil {
			http.Error(w, "bad page", http.StatusBadRequest)
			return
		}
		html, isOK := pageBody(page)
		if !isOK {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(html))
	}))
	t.Cleanup(srv.Close)
	return srv, requests
}

func disablePageDelay(t *testing.T) {
	t.Helper()
	saved := pageDelay
	pageDelay = 0
	t.Cleanup(func() { pageDelay = saved })
}

const titlePath = ".right_part .title a"

func TestFetchAllPages(t *testing.T) {
	disablePageDelay(t)

	opera := func(title string) eventCard {
		return eventCard{date: "01/12", startTime: "18:00", title: title, isOnSale: true}
	}

	tests := []struct {
		name         string
		pages        map[int][]eventCard // missing page = empty page
		failingPages []int
		trackPath    string
		maxPages     int
		want         []string
		wantRequests int32
		wantPartial  bool
		wantErr      bool
	}{
		{
			name:         "stops on empty page",
			pages:        map[int][]eventCard{0: {opera("A"), opera("B")}, 1: {opera("C")}},
			trackPath:    titlePath,
			maxPages:     20,
			want:         []string{"A", "B", "C"},
			wantRequests: 3,
		},
		{
			name:         "stops on page identical to previous",
			pages:        map[int][]eventCard{0: {opera("A")}, 1: {opera("B")}, 2: {opera("B")}, 3: {opera("never")}},
			trackPath:    titlePath,
			maxPages:     20,
			want:         []string{"A", "B"},
			wantRequests: 3,
		},
		{
			name:         "page without matches does not stop pagination",
			pages:        map[int][]eventCard{0: {opera("Кармен")}, 1: {opera("Gala концерт")}},
			trackPath:    titlePath + ":matches((?i)концерт)",
			maxPages:     20,
			want:         []string{"Gala концерт"},
			wantRequests: 3,
		},
		{
			name:         "stops at PAGE_MAX",
			pages:        map[int][]eventCard{0: {opera("A")}, 1: {opera("B")}, 2: {opera("C")}},
			trackPath:    titlePath,
			maxPages:     2,
			want:         []string{"A", "B"},
			wantRequests: 2,
		},
		{
			name:         "error on first page",
			failingPages: []int{0},
			trackPath:    titlePath,
			maxPages:     20,
			wantRequests: 1,
			wantErr:      true,
		},
		{
			name:         "error on later page is partial",
			pages:        map[int][]eventCard{0: {opera("A")}, 1: {opera("B")}, 3: {opera("never")}},
			failingPages: []int{2},
			trackPath:    titlePath,
			maxPages:     20,
			want:         []string{"A", "B"},
			wantRequests: 3,
			wantPartial:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, requests := pagedServer(t, func(page int) (string, bool) {
				if slices.Contains(tt.failingPages, page) {
					return "", false
				}
				return operaPage(tt.pages[page]...), true
			})

			got, isPartial, err := fetchAllPages(srv.URL+"/afisha?month=01-12-2026", nil, tt.trackPath, nil, titlePath, "page", tt.maxPages)

			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if isPartial != tt.wantPartial {
				t.Errorf("isPartial = %v, want %v", isPartial, tt.wantPartial)
			}
			if gotValues := slices.Sorted(maps.Keys(got)); !slices.Equal(gotValues, tt.want) {
				t.Errorf("values = %q, want %q", gotValues, tt.want)
			}
			if n := requests.Load(); n != tt.wantRequests {
				t.Errorf("requests = %d, want %d", n, tt.wantRequests)
			}
		})
	}
}

func TestFetchAllPagesWithFields(t *testing.T) {
	disablePageDelay(t)

	gala := "Новорічно-Різдвяний концерт"
	pages := map[int][]eventCard{
		0: {{date: "06/12", startTime: "15:00", title: "Чарівна флейта", isOnSale: true}, {date: "12/12", startTime: "12:00", title: gala, isOnSale: true}},
		1: {{date: "19/12", startTime: "15:00", title: gala, isOnSale: true}, {date: "20/12", startTime: "17:00", title: gala, isOnSale: false}},
	}
	srv, _ := pagedServer(t, func(page int) (string, bool) {
		return operaPage(pages[page]...), true
	})

	got, isPartial, err := fetchAllPages(
		srv.URL,
		nil,
		".item:has(.bay a):has(.title a:matches((?i)концерт))",
		[]string{".date", ".row_date b:first-of-type", ".title a"},
		titlePath,
		"page",
		20,
	)
	if err != nil || isPartial {
		t.Fatalf("err = %v, isPartial = %v", err, isPartial)
	}

	want := []string{
		"12/12 | 12:00 | " + gala,
		"19/12 | 15:00 | " + gala,
	}
	if gotValues := slices.Sorted(maps.Keys(got)); !slices.Equal(gotValues, want) {
		t.Errorf("values = %q, want %q", gotValues, want)
	}
}

func TestFetchSet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(operaPage(decemberCards...)))
	}))
	t.Cleanup(srv.Close)

	got, err := fetchSet(srv.URL, nil, titlePath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || !got["Новорічно-Різдвяний концерт"] {
		t.Errorf("got %v, want 4 unique titles", got)
	}
}

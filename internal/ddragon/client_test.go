package ddragon

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestVersionsAndCatalogNormalization(t *testing.T) {
	items := loadItemFixture(t)
	responses := map[string]string{
		"/api/versions.json":                        `["16.18.1","16.17.1"]`,
		"/cdn/16.18.1/data/es_MX/item.json":         items,
		"/cdn/16.18.1/data/en_US/championFull.json": championFixture,
	}
	client := testClient(t, responses, Config{})

	versions, err := client.Versions(context.Background())
	if err != nil || len(versions) != 2 || versions[0] != "16.18.1" {
		t.Fatalf("Versions() = %#v, %v", versions, err)
	}
	value, err := client.FetchCatalog(context.Background(), "16.18.1", "es_MX")
	if err != nil {
		t.Fatalf("FetchCatalog() error = %v", err)
	}
	item := value.Items[4401]
	if item.Name != "Fuerza de la naturaleza" || !item.Purchasable || !item.AvailableMap11 || item.Gold.Total != 2800 {
		t.Fatalf("normalized item = %#v", item)
	}
	if len(item.From) != 2 || item.From[0] != 1057 || item.Image.Full != "4401.png" {
		t.Fatalf("normalized recipe/image = %#v", item)
	}
	for _, placeholderID := range []int{2008, 226660, 772139, 772140} {
		if _, exists := value.Items[placeholderID]; exists {
			t.Fatalf("irrelevant official placeholder %d was retained", placeholderID)
		}
	}
	champion := value.Champions["Garen"]
	if champion.Key != "86" || len(champion.Spells) != 1 || !strings.Contains(champion.Spells[0].Tooltip, "physicalDamage") {
		t.Fatalf("normalized champion = %#v", champion)
	}
}

func TestVersionsFiltersLegacyAliasesAndPreservesServerOrder(t *testing.T) {
	payload, err := os.ReadFile("testdata/versions-with-legacy.json")
	if err != nil {
		t.Fatalf("read versions regression fixture: %v", err)
	}
	client := testClient(t, map[string]string{
		"/api/versions.json": string(payload),
	}, Config{})

	versions, err := client.Versions(context.Background())
	if err != nil {
		t.Fatalf("Versions() error = %v", err)
	}
	want := []string{"16.18.1", "16.17.1"}
	if !reflect.DeepEqual(versions, want) {
		t.Fatalf("Versions() = %#v, want %#v", versions, want)
	}
	if versions[0] != "16.18.1" {
		t.Fatalf("newest valid version = %q, want 16.18.1", versions[0])
	}
}

func TestVersionsRejectsAllInvalidEntries(t *testing.T) {
	client := testClient(t, map[string]string{
		"/api/versions.json": `["lolpatch_7.20","invalid","16.18","16.18.x"]`,
	}, Config{})

	versions, err := client.Versions(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no valid semantic patches") {
		t.Fatalf("Versions() = %#v, %v; want all-invalid error", versions, err)
	}
}

func TestCatalogRejectsDanglingRelevantRecipeEdge(t *testing.T) {
	brokenItems := strings.Replace(loadItemFixture(t), `"from": ["1057", "1028"]`, `"from": ["1057", "9999"]`, 1)
	client := testClient(t, map[string]string{
		"/cdn/16.18.1/data/es_MX/item.json":         brokenItems,
		"/cdn/16.18.1/data/en_US/championFull.json": championFixture,
	}, Config{})
	if _, err := client.FetchCatalog(context.Background(), "16.18.1", "es_MX"); err == nil || !strings.Contains(err.Error(), "missing recipe item 9999") {
		t.Fatalf("FetchCatalog() error = %v, want dangling recipe rejection", err)
	}
}

func TestBlankRecordWithRecipeEdgeIsNotSilentlyFiltered(t *testing.T) {
	connectedPlaceholder := strings.Replace(loadItemFixture(t), `"stacks": 9,`, `"from": ["1028"], "stacks": 9,`, 1)
	client := testClient(t, map[string]string{
		"/cdn/16.18.1/data/es_MX/item.json":         connectedPlaceholder,
		"/cdn/16.18.1/data/en_US/championFull.json": championFixture,
	}, Config{})
	if _, err := client.FetchCatalog(context.Background(), "16.18.1", "es_MX"); err == nil || !strings.Contains(err.Error(), "item 2008 has invalid required fields") {
		t.Fatalf("FetchCatalog() error = %v, want connected blank-node rejection", err)
	}
}

func TestHTTPAndPayloadValidation(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		contentType string
		body        string
		maxBytes    int64
	}{
		{name: "non-200", status: http.StatusBadGateway, contentType: "application/json", body: `{}`},
		{name: "non-json", status: http.StatusOK, contentType: "text/html", body: `<html></html>`},
		{name: "empty schema", status: http.StatusOK, contentType: "application/json", body: `[]`},
		{name: "trailing json", status: http.StatusOK, contentType: "application/json", body: `["16.18.1"] {}`},
		{name: "oversized", status: http.StatusOK, contentType: "application/json", body: strings.Repeat(" ", 33), maxBytes: 32},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return response(request, test.status, test.contentType, test.body), nil
			})
			client, err := New("https://ddragon.test", &http.Client{Transport: transport}, Config{MaxResponseBytes: test.maxBytes})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			if _, err := client.Versions(context.Background()); err == nil {
				t.Fatal("Versions() unexpectedly succeeded")
			}
		})
	}
}

func testClient(t *testing.T, responses map[string]string, cfg Config) *Client {
	t.Helper()
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, exists := responses[request.URL.Path]
		if !exists {
			return nil, errors.New("unexpected path: " + request.URL.Path)
		}
		return response(request, http.StatusOK, "application/json; charset=utf-8", body), nil
	})
	client, err := New("https://ddragon.test", &http.Client{Transport: transport}, cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return client
}

func response(request *http.Request, status int, contentType, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func loadItemFixture(t *testing.T) string {
	t.Helper()
	payload, err := os.ReadFile("testdata/item-16.18.1-blank-placeholders.json")
	if err != nil {
		t.Fatalf("read Data Dragon regression fixture: %v", err)
	}
	return string(payload)
}

const championFixture = `{
  "type":"champion","version":"16.18.1","data":{
    "Garen":{
      "id":"Garen","key":"86","name":"Garen","tags":["Fighter","Tank"],
      "passive":{"name":"Perseverance","description":"Garen regenerates health."},
      "spells":[{"name":"Decisive Strike","description":"Deals physical damage.","tooltip":"Deals <physicalDamage>damage</physicalDamage>."}]
    }
  }
}`

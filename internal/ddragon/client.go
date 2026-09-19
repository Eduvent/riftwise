package ddragon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"lolengine/internal/catalog"
)

const defaultMaxResponseBytes int64 = 16 << 20

// Config controls Data Dragon HTTP safety limits.
type Config struct {
	Timeout          time.Duration
	MaxResponseBytes int64
}

// Client retrieves and normalizes Data Dragon data.
type Client struct {
	baseURL          *url.URL
	httpClient       *http.Client
	maxResponseBytes int64
	now              func() time.Time
}

// New creates a Data Dragon client. The caller may inject an in-memory HTTP client for tests.
func New(baseURL string, httpClient *http.Client, cfg Config) (*Client, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return nil, fmt.Errorf("parse Data Dragon base URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("Data Dragon base URL must use http or https")
	}
	if parsed.Hostname() == "" || parsed.User != nil {
		return nil, errors.New("Data Dragon base URL must contain a host and no user information")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Second
	}
	if cfg.MaxResponseBytes <= 0 {
		cfg.MaxResponseBytes = defaultMaxResponseBytes
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: cfg.Timeout}
	} else if httpClient.Timeout == 0 {
		clone := *httpClient
		clone.Timeout = cfg.Timeout
		httpClient = &clone
	}
	return &Client{
		baseURL:          parsed,
		httpClient:       httpClient,
		maxResponseBytes: cfg.MaxResponseBytes,
		now:              func() time.Time { return time.Now().UTC() },
	}, nil
}

// Versions returns Data Dragon versions in server order, newest first.
func (client *Client) Versions(ctx context.Context) ([]string, error) {
	var values []string
	if err := client.getJSON(ctx, "/api/versions.json", &values); err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, errors.New("Data Dragon versions response is empty")
	}
	versions := make([]string, 0, len(values))
	for _, value := range values {
		if version, ok := semanticPatch(value); ok {
			versions = append(versions, version)
		}
	}
	if len(versions) == 0 {
		return nil, errors.New("Data Dragon versions response contains no valid semantic patches")
	}
	return versions, nil
}

func semanticPatch(value string) (string, bool) {
	value = strings.TrimSpace(value)
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return "", false
	}
	for _, part := range parts {
		if part == "" {
			return "", false
		}
		for _, character := range part {
			if character < '0' || character > '9' {
				return "", false
			}
		}
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 {
			return "", false
		}
	}
	return value, true
}

// FetchCatalog loads localized items and English champion spell markup for one patch.
func (client *Client) FetchCatalog(ctx context.Context, patch, locale string) (catalog.Catalog, error) {
	if catalog.MajorMinor(patch) == "" {
		return catalog.Catalog{}, fmt.Errorf("invalid patch %q", patch)
	}
	if strings.TrimSpace(locale) == "" {
		return catalog.Catalog{}, errors.New("item locale must not be empty")
	}

	var items itemDocument
	itemPath := fmt.Sprintf("/cdn/%s/data/%s/item.json", url.PathEscape(patch), url.PathEscape(locale))
	if err := client.getJSON(ctx, itemPath, &items); err != nil {
		return catalog.Catalog{}, fmt.Errorf("fetch items: %w", err)
	}
	var champions championDocument
	championPath := fmt.Sprintf("/cdn/%s/data/en_US/championFull.json", url.PathEscape(patch))
	if err := client.getJSON(ctx, championPath, &champions); err != nil {
		return catalog.Catalog{}, fmt.Errorf("fetch champions: %w", err)
	}

	value, err := normalizeCatalog(patch, locale, items, champions)
	if err != nil {
		return catalog.Catalog{}, err
	}
	value.SyncedAt = client.now()
	return value, nil
}

func (client *Client) getJSON(ctx context.Context, requestPath string, target any) error {
	endpoint := *client.baseURL
	endpoint.Path = strings.TrimRight(client.baseURL.Path, "/") + requestPath
	endpoint.RawPath = ""
	endpoint.RawQuery = ""
	endpoint.Fragment = ""
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return err
	}
	resp, err := client.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request %s: %w", requestPath, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("request %s returned HTTP %d", requestPath, resp.StatusCode)
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return fmt.Errorf("request %s returned non-JSON content type %q", requestPath, resp.Header.Get("Content-Type"))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, client.maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read %s: %w", requestPath, err)
	}
	if int64(len(body)) > client.maxResponseBytes {
		return fmt.Errorf("response %s exceeds %d bytes", requestPath, client.maxResponseBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode %s: %w", requestPath, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("response %s contains trailing JSON or data", requestPath)
	}
	return nil
}

type itemDocument struct {
	Type    string                 `json:"type"`
	Version string                 `json:"version"`
	Data    map[string]itemPayload `json:"data"`
}

type itemPayload struct {
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Plaintext   string             `json:"plaintext"`
	From        []string           `json:"from"`
	Into        []string           `json:"into"`
	Tags        []string           `json:"tags"`
	Maps        map[string]bool    `json:"maps"`
	Gold        goldPayload        `json:"gold"`
	Stats       map[string]float64 `json:"stats"`
	Image       imagePayload       `json:"image"`
}

type goldPayload struct {
	Base        int  `json:"base"`
	Total       int  `json:"total"`
	Sell        int  `json:"sell"`
	Purchasable bool `json:"purchasable"`
}

type imagePayload struct {
	Full   string `json:"full"`
	Sprite string `json:"sprite"`
	Group  string `json:"group"`
	X      int    `json:"x"`
	Y      int    `json:"y"`
	W      int    `json:"w"`
	H      int    `json:"h"`
}

type championDocument struct {
	Type    string                     `json:"type"`
	Version string                     `json:"version"`
	Data    map[string]championPayload `json:"data"`
}

type championPayload struct {
	ID      string           `json:"id"`
	Key     string           `json:"key"`
	Name    string           `json:"name"`
	Tags    []string         `json:"tags"`
	Passive abilityPayload   `json:"passive"`
	Spells  []abilityPayload `json:"spells"`
}

type abilityPayload struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Tooltip     string `json:"tooltip"`
}

func normalizeCatalog(patch, locale string, items itemDocument, champions championDocument) (catalog.Catalog, error) {
	if items.Type != "item" || items.Version != patch {
		return catalog.Catalog{}, fmt.Errorf("item document metadata does not match patch %s", patch)
	}
	if champions.Type != "champion" || champions.Version != patch {
		return catalog.Catalog{}, fmt.Errorf("champion document metadata does not match patch %s", patch)
	}
	if len(items.Data) == 0 {
		return catalog.Catalog{}, errors.New("item document contains no data")
	}
	if len(champions.Data) == 0 {
		return catalog.Catalog{}, errors.New("champion document contains no data")
	}
	result := catalog.Catalog{
		Patch:     patch,
		Locale:    locale,
		Items:     make(map[int]catalog.Item, len(items.Data)),
		Champions: make(map[string]catalog.Champion, len(champions.Data)),
	}
	for rawID, item := range items.Data {
		id, err := strconv.Atoi(rawID)
		if err != nil || id <= 0 {
			return catalog.Catalog{}, fmt.Errorf("invalid item ID %q", rawID)
		}
		// Official Data Dragon item documents contain blank, non-map placeholder
		// records used by other game systems. They are not shop/recipe nodes and
		// must not make a Summoner's Rift catalog unusable. A blank record with a
		// map-11 flag or recipe edge is retained so validation fails loudly rather
		// than silently discarding a relevant node.
		if irrelevantPlaceholder(item) {
			continue
		}
		if strings.TrimSpace(item.Name) == "" || item.Gold.Total < 0 || item.Gold.Base < 0 {
			return catalog.Catalog{}, fmt.Errorf("item %d has invalid required fields", id)
		}
		from, err := parseItemIDs(item.From)
		if err != nil {
			return catalog.Catalog{}, fmt.Errorf("item %d from recipe: %w", id, err)
		}
		into, err := parseItemIDs(item.Into)
		if err != nil {
			return catalog.Catalog{}, fmt.Errorf("item %d into recipe: %w", id, err)
		}
		result.Items[id] = catalog.Item{
			ID:             id,
			Name:           item.Name,
			Description:    item.Description,
			Plaintext:      item.Plaintext,
			Purchasable:    item.Gold.Purchasable,
			AvailableMap11: item.Maps["11"],
			Gold: catalog.Gold{
				Base: item.Gold.Base, Total: item.Gold.Total, Sell: item.Gold.Sell, Purchasable: item.Gold.Purchasable,
			},
			From:  from,
			Into:  into,
			Tags:  append([]string(nil), item.Tags...),
			Stats: cloneStats(item.Stats),
			Image: catalog.Image{
				Full: item.Image.Full, Sprite: item.Image.Sprite, Group: item.Image.Group,
				X: item.Image.X, Y: item.Image.Y, W: item.Image.W, H: item.Image.H,
			},
		}
	}
	for key, champion := range champions.Data {
		if champion.ID == "" || champion.Key == "" || champion.Name == "" {
			return catalog.Catalog{}, fmt.Errorf("champion %q has invalid required fields", key)
		}
		spells := make([]catalog.AbilityText, 0, len(champion.Spells))
		for _, spell := range champion.Spells {
			spells = append(spells, catalog.AbilityText{Name: spell.Name, Description: spell.Description, Tooltip: spell.Tooltip})
		}
		result.Champions[champion.ID] = catalog.Champion{
			ID:   champion.ID,
			Key:  champion.Key,
			Name: champion.Name,
			Tags: append([]string(nil), champion.Tags...),
			Passive: catalog.AbilityText{
				Name: champion.Passive.Name, Description: champion.Passive.Description, Tooltip: champion.Passive.Tooltip,
			},
			Spells: spells,
		}
	}
	if err := result.Validate(); err != nil {
		return catalog.Catalog{}, err
	}
	return result, nil
}

func irrelevantPlaceholder(item itemPayload) bool {
	return strings.TrimSpace(item.Name) == "" && !item.Maps["11"] && len(item.From) == 0 && len(item.Into) == 0
}

func parseItemIDs(values []string) ([]int, error) {
	result := make([]int, 0, len(values))
	for _, value := range values {
		id, err := strconv.Atoi(value)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("invalid item ID %q", value)
		}
		result = append(result, id)
	}
	return result, nil
}

func cloneStats(source map[string]float64) map[string]float64 {
	if source == nil {
		return map[string]float64{}
	}
	clone := make(map[string]float64, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

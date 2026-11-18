package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Hubs and temples are owned by kratos. This service only reads them.

const unassignedHubID = "unassigned"

type Hub struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Temple struct {
	ID    string `json:"id"`
	HubID string `json:"hub_id"`
	Name  string `json:"name"`
}

type CatalogData struct {
	Hubs    []Hub    `json:"hubs"`
	Temples []Temple `json:"temples"`
}

func (c *CatalogData) normalise() {
	needUnassigned := false
	known := map[string]bool{}
	for _, h := range c.Hubs {
		known[h.ID] = true
	}
	for i := range c.Temples {
		if c.Temples[i].HubID == "" || !known[c.Temples[i].HubID] {
			c.Temples[i].HubID = unassignedHubID
			needUnassigned = true
		}
		if c.Temples[i].Name == "" {
			c.Temples[i].Name = "Temple " + short(c.Temples[i].ID)
		}
	}
	if needUnassigned {
		c.Hubs = append(c.Hubs, Hub{ID: unassignedHubID, Name: "No hub assigned"})
	}
	sort.SliceStable(c.Hubs, func(i, j int) bool { return c.Hubs[i].Name < c.Hubs[j].Name })
	sort.SliceStable(c.Temples, func(i, j int) bool { return c.Temples[i].Name < c.Temples[j].Name })
}

func (c *CatalogData) hub(id string) (Hub, bool) {
	for _, h := range c.Hubs {
		if h.ID == id {
			return h, true
		}
	}
	return Hub{}, false
}

func (c *CatalogData) temple(id string) (Temple, bool) {
	for _, t := range c.Temples {
		if t.ID == id {
			return t, true
		}
	}
	return Temple{}, false
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

type Catalog interface {
	Load(ctx context.Context) (*CatalogData, error)
}

// ---- seed file: for local development without kratos access ----

type seedCatalog struct{ path string }

func (s seedCatalog) Load(context.Context) (*CatalogData, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return nil, fmt.Errorf("read catalog seed file: %w", err)
	}
	var data CatalogData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("parse catalog seed file: %w", err)
	}
	data.normalise()
	return &data, nil
}

// ---- kratos: read-only calls to the existing internal APIs ----
//   GET /kratos/internal/v1/hub      -> { count, hubs: [{id, name, active, is_deleted}] }
//   GET /kratos/internal/v1/temples  -> { count, temples: [{id, hub_id, active, temple_content: [{title, language}]}] }

type kratosCatalog struct {
	base, lang, header string
	client             *http.Client
}

func (k kratosCatalog) get(ctx context.Context, path string, q url.Values, key string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, k.base+path+"?"+q.Encode(), nil)
	if err != nil {
		return 0, err
	}
	if name, value, ok := strings.Cut(k.header, ":"); ok {
		req.Header.Set(strings.TrimSpace(name), strings.TrimSpace(value))
	}
	resp, err := k.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("kratos %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("kratos %s: status %d", path, resp.StatusCode)
	}
	var body any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, fmt.Errorf("kratos %s: %w", path, err)
	}
	// The payload sits inside a response envelope; find the object that holds `key`.
	holder := findHolder(body, key, 3)
	if holder == nil {
		return 0, fmt.Errorf("kratos %s: no %q field in response", path, key)
	}
	raw, _ := json.Marshal(holder[key])
	if err := json.Unmarshal(raw, out); err != nil {
		return 0, fmt.Errorf("kratos %s: %w", path, err)
	}
	count := 0
	if c, ok := holder["count"].(float64); ok {
		count = int(c)
	}
	return count, nil
}

func findHolder(v any, key string, depth int) map[string]any {
	m, ok := v.(map[string]any)
	if !ok || depth == 0 {
		return nil
	}
	if _, ok := m[key]; ok {
		return m
	}
	for _, child := range m {
		if h := findHolder(child, key, depth-1); h != nil {
			return h
		}
	}
	return nil
}

func (k kratosCatalog) Load(ctx context.Context) (*CatalogData, error) {
	const page = 100
	data := &CatalogData{}

	for offset := 0; ; offset += page {
		var hubs []struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			IsDeleted bool   `json:"is_deleted"`
		}
		q := url.Values{"limit": {fmt.Sprint(page)}, "offset": {fmt.Sprint(offset)}}
		if _, err := k.get(ctx, "/kratos/internal/v1/hub", q, "hubs", &hubs); err != nil {
			return nil, err
		}
		for _, h := range hubs {
			if !h.IsDeleted {
				data.Hubs = append(data.Hubs, Hub{ID: h.ID, Name: h.Name})
			}
		}
		if len(hubs) < page {
			break
		}
	}

	for offset := 0; ; offset += page {
		var temples []struct {
			ID        string  `json:"id"`
			HubID     *string `json:"hub_id"`
			IsDeleted bool    `json:"is_deleted"`
			Content   []struct {
				Title    *string `json:"title"`
				Language string  `json:"language"`
			} `json:"temple_content"`
		}
		q := url.Values{"limit": {fmt.Sprint(page)}, "offset": {fmt.Sprint(offset)}, "info": {"high"}, "lang": {k.lang}}
		if _, err := k.get(ctx, "/kratos/internal/v1/temples", q, "temples", &temples); err != nil {
			return nil, err
		}
		for _, t := range temples {
			if t.IsDeleted {
				continue
			}
			temple := Temple{ID: t.ID}
			if t.HubID != nil {
				temple.HubID = *t.HubID
			}
			for _, c := range t.Content {
				if c.Title != nil && *c.Title != "" && (temple.Name == "" || c.Language == k.lang) {
					temple.Name = *c.Title
				}
			}
			data.Temples = append(data.Temples, temple)
		}
		if len(temples) < page {
			break
		}
	}
	data.normalise()
	return data, nil
}

// ---- cache so dashboards do not hammer kratos ----

type cachedCatalog struct {
	inner Catalog
	ttl   time.Duration
	mu    sync.Mutex
	data  *CatalogData
	at    time.Time
}

func (c *cachedCatalog) Load(ctx context.Context) (*CatalogData, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.data != nil && time.Since(c.at) < c.ttl {
		return c.data, nil
	}
	data, err := c.inner.Load(ctx)
	if err != nil {
		if c.data != nil {
			return c.data, nil // serve the last good copy if kratos is briefly unreachable
		}
		return nil, err
	}
	c.data, c.at = data, time.Now()
	return data, nil
}

func newCatalog(cfg Config) (Catalog, error) {
	switch cfg.CatalogSource {
	case "seed":
		return &cachedCatalog{inner: seedCatalog{cfg.SeedFile}, ttl: 10 * time.Second}, nil
	case "kratos":
		if cfg.KratosBaseURL == "" {
			return nil, fmt.Errorf("CATALOG_SOURCE=kratos needs KRATOS_BASE_URL")
		}
		k := kratosCatalog{base: cfg.KratosBaseURL, lang: cfg.KratosLang, header: cfg.KratosHeader,
			client: &http.Client{Timeout: 20 * time.Second}}
		return &cachedCatalog{inner: k, ttl: 5 * time.Minute}, nil
	}
	return nil, fmt.Errorf("unknown CATALOG_SOURCE %q (use seed or kratos)", cfg.CatalogSource)
}

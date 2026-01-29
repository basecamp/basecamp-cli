package presenter

import (
	"embed"
	"fmt"
	"sync"

	"gopkg.in/yaml.v3"
)

//go:embed schemas/*.yaml
var schemasFS embed.FS

// registry is the singleton schema registry.
var registry = &Registry{}

// Registry holds loaded entity schemas indexed by name and type key.
type Registry struct {
	once    sync.Once
	byName  map[string]*EntitySchema // "todo" → schema
	byType  map[string]*EntitySchema // "Todo" → schema
	loadErr error
}

// load parses all embedded YAML schemas.
func (r *Registry) load() {
	r.once.Do(func() {
		r.byName = make(map[string]*EntitySchema)
		r.byType = make(map[string]*EntitySchema)

		entries, err := schemasFS.ReadDir("schemas")
		if err != nil {
			r.loadErr = fmt.Errorf("reading schemas dir: %w", err)
			return
		}

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}

			data, err := schemasFS.ReadFile("schemas/" + entry.Name())
			if err != nil {
				continue
			}

			schema := new(EntitySchema)
			if err := yaml.Unmarshal(data, schema); err != nil {
				continue
			}

			r.byName[schema.Entity] = schema
			if schema.TypeKey != "" {
				r.byType[schema.TypeKey] = schema
			}
		}
	})
}

// LookupByName returns a schema by entity name (e.g. "todo").
func LookupByName(name string) *EntitySchema {
	registry.load()
	return registry.byName[name]
}

// LookupByTypeKey returns a schema by API type key (e.g. "Todo").
func LookupByTypeKey(typeKey string) *EntitySchema {
	registry.load()
	return registry.byType[typeKey]
}

// Detect finds a schema from data. It checks an explicit entity name hint first,
// then falls back to auto-detection from the data's "type" field.
func Detect(data any, entityHint string) *EntitySchema {
	// Try explicit hint first
	if entityHint != "" {
		if s := LookupByName(entityHint); s != nil {
			return s
		}
	}

	// Try auto-detection from data's "type" field
	switch d := data.(type) {
	case map[string]any:
		if typeKey, ok := d["type"].(string); ok {
			if s := LookupByTypeKey(typeKey); s != nil {
				return s
			}
		}
	case []map[string]any:
		if len(d) > 0 {
			if typeKey, ok := d[0]["type"].(string); ok {
				if s := LookupByTypeKey(typeKey); s != nil {
					return s
				}
			}
		}
	}

	return nil
}

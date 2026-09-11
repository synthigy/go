package synthigy

import (
	"context"
	"strings"
	"sync"
)

// schemaResolver caches the IAM-projected schema and the xid maps it carries:
// attr-xid -> attribute name, entity name -> entity-xid, and relation xids per
// entity. Used by the watch query primitives to derive relation interest.
type schemaResolver struct {
	client *Client

	mu            sync.Mutex
	loaded        bool
	attrXidToName map[string]string
	nameToTable   map[string]string
	relsByEntity  map[string][]string
}

func newSchemaResolver(c *Client) *schemaResolver {
	return &schemaResolver{
		client:        c,
		attrXidToName: map[string]string{},
		nameToTable:   map[string]string{},
		relsByEntity:  map[string][]string{},
	}
}

func (r *schemaResolver) ensureLoaded(ctx context.Context) error {
	r.mu.Lock()
	if r.loaded {
		r.mu.Unlock()
		return nil
	}
	r.mu.Unlock()
	return r.refresh(ctx)
}

func (r *schemaResolver) refresh(ctx context.Context) error {
	schema, err := r.client.Schema(ctx)
	if err != nil {
		return err
	}
	attr := map[string]string{}
	nameToTable := map[string]string{}
	rels := map[string][]string{}

	entities, _ := schema["entities"].(map[string]any)
	for name, raw := range entities {
		entity, _ := raw.(map[string]any)
		if entity == nil {
			continue
		}
		xid := asStr(entity["xid"])
		if xid != "" {
			nameToTable[name] = xid
		}
		xids, _ := entity["xids"].(map[string]any)
		if xids == nil {
			continue
		}
		if attrXids, ok := xids["attributes"].(map[string]any); ok {
			for attrName, ax := range attrXids {
				if s := asStr(ax); s != "" {
					attr[s] = attrName
				}
			}
		}
		if relXids, ok := xids["relations"].(map[string]any); ok && xid != "" {
			set := map[string]struct{}{}
			for _, x := range r.relsByEntity[xid] {
				set[x] = struct{}{}
			}
			for _, rx := range relXids {
				if s := asStr(rx); s != "" {
					set[s] = struct{}{}
				}
			}
			rels[xid] = sortedKeys(set)
		}
	}

	r.mu.Lock()
	r.attrXidToName = attr
	r.nameToTable = nameToTable
	r.relsByEntity = rels
	r.loaded = true
	r.mu.Unlock()
	return nil
}

func (r *schemaResolver) attrName(xid string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n, ok := r.attrXidToName[xid]; ok {
		return n
	}
	return xid
}

func (r *schemaResolver) entityXidByName(name string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.nameToTable[kebab(name)]
}

func (r *schemaResolver) relationsForEntity(entityXid string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if entityXid == "" {
		return nil
	}
	return append([]string(nil), r.relsByEntity[entityXid]...)
}

func asStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// kebab converts camelCase / snake_case / spaced names to kebab-case.
func kebab(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	prevLowerOrDigit := false
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
			if prevLowerOrDigit {
				b.WriteByte('-')
			}
			b.WriteRune(r - 'A' + 'a')
			prevLowerOrDigit = false
		case r == ' ' || r == '_' || r == '-':
			b.WriteByte('-')
			prevLowerOrDigit = false
		default:
			b.WriteRune(r)
			prevLowerOrDigit = (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		}
	}
	// Collapse any accidental double dashes.
	return strings.Trim(collapseDashes(b.String()), "-")
}

func collapseDashes(s string) string {
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return s
}

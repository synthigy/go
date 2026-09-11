package synthigy

import "strings"

// ComposeTree composes a flat list of records into a single tree rooted at
// rootID, linking children to parents via the `on` relation. Each record
// must include the parent-FK relation in its selection. childrenKey defaults
// to "_children" when empty. Returns nil if the root isn't found.
func ComposeTree(records []Record, on, rootID, childrenKey string) Record {
	if len(records) == 0 || on == "" {
		return nil
	}
	if childrenKey == "" {
		childrenKey = "_children"
	}
	byID, kids := buildIndexes(records, on)
	root := rootID
	if root == "" {
		root = recordID(records[0])
	}
	if _, ok := byID[root]; !ok {
		return nil
	}
	return buildSubtree(byID, kids, childrenKey, root, map[string]bool{})
}

// ComposeForest composes a flat list into a forest — one tree per record
// whose parent isn't present in the set. Useful for search-tree results.
// childrenKey defaults to "_children" when empty.
func ComposeForest(records []Record, on, childrenKey string) []Record {
	if len(records) == 0 || on == "" {
		return []Record{}
	}
	if childrenKey == "" {
		childrenKey = "_children"
	}
	byID, kids := buildIndexes(records, on)
	out := []Record{}
	for _, r := range records {
		id := recordID(r)
		if id == "" {
			continue
		}
		pid := parentID(r, on)
		if pid == "" || pid == id {
			out = append(out, buildSubtree(byID, kids, childrenKey, id, map[string]bool{}))
			continue
		}
		if _, ok := byID[pid]; !ok {
			out = append(out, buildSubtree(byID, kids, childrenKey, id, map[string]bool{}))
		}
	}
	return out
}

// buildIndexes returns record-by-id and parent->child-ids maps. Two-pass,
// id-level linking avoids stale snapshots when a child is mutated after being
// copied into a parent.
func buildIndexes(records []Record, on string) (map[string]Record, map[string][]string) {
	byID := make(map[string]Record, len(records))
	kids := map[string][]string{}
	for _, r := range records {
		id := recordID(r)
		if id == "" {
			continue
		}
		byID[id] = r
		pid := parentID(r, on)
		if pid != "" && pid != id {
			kids[pid] = append(kids[pid], id)
		}
	}
	return byID, kids
}

func buildSubtree(byID map[string]Record, kids map[string][]string, childrenKey, id string, visited map[string]bool) Record {
	if visited[id] {
		return nil // cycle — break
	}
	visited[id] = true
	src := byID[id]
	out := make(Record, len(src)+1)
	for k, v := range src {
		out[k] = v
	}
	children := []Record{}
	for _, cid := range kids[id] {
		if sub := buildSubtree(byID, kids, childrenKey, cid, visited); sub != nil {
			children = append(children, sub)
		}
	}
	out[childrenKey] = children
	delete(visited, id)
	return out
}

// recordID reads the id off a record, trying xid, euuid, then _eid.
func recordID(r Record) string {
	if r == nil {
		return ""
	}
	for _, k := range []string{"xid", "euuid", "_eid"} {
		if v, ok := r[k]; ok {
			if s := asIDString(v); s != "" {
				return s
			}
		}
	}
	return ""
}

// parentID reads the parent id via the `on` relation, accepting kebab/snake
// casings and either a nested relation object or a bare id string.
func parentID(r Record, on string) string {
	if r == nil {
		return ""
	}
	variants := []string{on, strings.ReplaceAll(on, "-", "_"), strings.ReplaceAll(on, "_", "-")}
	for _, k := range variants {
		v, ok := r[k]
		if !ok {
			continue
		}
		if v == nil {
			return ""
		}
		if m, ok := v.(map[string]any); ok {
			return recordID(Record(m))
		}
		return asIDString(v)
	}
	return ""
}

func asIDString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

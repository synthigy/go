package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// warnStaleSchema prints one stderr line when schemaPath's deploy stamp is
// older than the model the server publishes at /.well-known/synthigy. Silent
// on any failure: generation stays offline-capable.
func warnStaleSchema(w io.Writer, endpoint, schemaPath string) {
	if endpoint == "" {
		return
	}
	raw, err := os.ReadFile(schemaPath)
	if err != nil {
		return
	}
	var local struct {
		Datasets map[string]struct {
			DeployedAt string `json:"deployed-at"`
		} `json:"datasets"`
	}
	if json.Unmarshal(raw, &local) != nil {
		return
	}
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(strings.TrimSuffix(endpoint, "/") + "/.well-known/synthigy")
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	var remote struct {
		Datasets map[string]string `json:"datasets"`
	}
	if json.NewDecoder(resp.Body).Decode(&remote) != nil {
		return
	}
	var changed []string
	for id, at := range remote.Datasets {
		l, ok := local.Datasets[id]
		if !ok || !sameInstant(l.DeployedAt, at) {
			changed = append(changed, id)
		}
	}
	for id := range local.Datasets {
		if _, ok := remote.Datasets[id]; !ok {
			changed = append(changed, id)
		}
	}
	if len(changed) == 0 {
		return
	}
	sort.Strings(changed)
	fmt.Fprintf(w, "warning: %s is stale against %s (%d datasets changed since the pull) — run: synthigy-gen -pull\n",
		schemaPath, endpoint, len(changed))
}

func sameInstant(a, b string) bool {
	ta, errA := time.Parse(time.RFC3339Nano, a)
	tb, errB := time.Parse(time.RFC3339Nano, b)
	if errA != nil || errB != nil {
		return a == b
	}
	return ta.Equal(tb)
}

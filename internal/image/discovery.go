package image

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

type Candidate struct {
	Reference   string `json:"reference"`
	Source      string `json:"source"`
	TrustScore  int    `json:"trustScore"`
	TrustReason string `json:"trustReason"`
	Description string `json:"description,omitempty"`
}

func DiscoverTrusted(ctx context.Context, current string, target Platform, limit int) ([]Candidate, error) {
	if limit <= 0 {
		limit = 5
	}
	parsed, err := parseReference(current)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	candidates := []Candidate{}
	if sameRepo, err := FindCompatible(ctx, current, target, limit); err == nil {
		for _, result := range sameRepo {
			candidates = append(candidates, Candidate{
				Reference:   result.PinnedReference(),
				Source:      "same-repository",
				TrustScore:  100,
				TrustReason: "same repository with verified target platform",
			})
		}
	}
	if len(candidates) < limit {
		public, err := discoverDockerHub(ctx, parsed.Repository, target, limit-len(candidates))
		if err == nil {
			candidates = append(candidates, public...)
		}
	}
	candidates = dedupeCandidates(candidates)
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].TrustScore != candidates[j].TrustScore {
			return candidates[i].TrustScore > candidates[j].TrustScore
		}
		return candidates[i].Reference < candidates[j].Reference
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no trusted public image candidate supports %s", target.String())
	}
	return candidates, nil
}

func discoverDockerHub(ctx context.Context, repository string, target Platform, limit int) ([]Candidate, error) {
	query := imageLeaf(repository)
	if query == "" {
		return nil, fmt.Errorf("cannot derive image search query")
	}
	u, _ := url.Parse("https://hub.docker.com/v2/search/repositories/")
	params := u.Query()
	params.Set("query", query)
	params.Set("page_size", "25")
	u.RawQuery = params.Encode()
	client := &http.Client{Timeout: 8 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("Docker Hub search returned HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Results []struct {
			Name        string `json:"repo_name"`
			Description string `json:"short_description"`
			Stars       int    `json:"star_count"`
			Pulls       int64  `json:"pull_count"`
			Official    bool   `json:"is_official"`
			Verified    bool   `json:"is_verified"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	results := []Candidate{}
	for _, item := range payload.Results {
		if len(results) >= limit || !similarRepository(item.Name, query) {
			continue
		}
		reference := strings.TrimSpace(item.Name)
		if reference == "" {
			continue
		}
		manifest, err := Inspect(ctx, reference)
		if err != nil || !manifest.Supports(target.OS, target.Architecture) {
			continue
		}
		score, reason := dockerHubTrust(item.Official, item.Verified, item.Pulls, item.Stars)
		results = append(results, Candidate{Reference: manifest.PinnedReference(), Source: "docker-hub", TrustScore: score, TrustReason: reason, Description: trimDescription(item.Description)})
	}
	return results, nil
}

func imageLeaf(repository string) string {
	parts := strings.Split(strings.TrimSpace(repository), "/")
	if len(parts) == 0 {
		return ""
	}
	return strings.TrimSpace(parts[len(parts)-1])
}

func similarRepository(repository, query string) bool {
	leaf := imageLeaf(repository)
	leaf = strings.ToLower(strings.ReplaceAll(leaf, "_", "-"))
	query = strings.ToLower(strings.ReplaceAll(query, "_", "-"))
	return leaf == query || strings.Contains(leaf, query) || strings.Contains(query, leaf)
}

func dockerHubTrust(official, verified bool, pulls int64, stars int) (int, string) {
	switch {
	case official:
		return 95, "Docker Official Image with verified target platform"
	case verified:
		return 88, "Docker Verified Publisher with verified target platform"
	case pulls >= 10_000_000 && stars >= 100:
		return 75, "well-adopted public image with verified target platform"
	case pulls >= 1_000_000:
		return 65, "public image with substantial pulls and verified target platform"
	default:
		return 45, "public image with verified target platform"
	}
}

func dedupeCandidates(values []Candidate) []Candidate {
	seen := map[string]bool{}
	out := make([]Candidate, 0, len(values))
	for _, value := range values {
		if value.Reference == "" || seen[value.Reference] {
			continue
		}
		seen[value.Reference] = true
		out = append(out, value)
	}
	return out
}

func trimDescription(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= 180 {
		return value
	}
	return value[:177] + "..."
}

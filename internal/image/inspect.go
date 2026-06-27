package image

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"sort"
	"strings"
	"time"
)

type Platform struct {
	OS           string `json:"os,omitempty"`
	Architecture string `json:"architecture,omitempty"`
	Variant      string `json:"variant,omitempty"`
}

func (p Platform) String() string {
	parts := []string{p.OS, p.Architecture}
	if p.Variant != "" {
		parts = append(parts, p.Variant)
	}
	return strings.Trim(strings.Join(parts, "/"), "/")
}

type Result struct {
	Reference string     `json:"reference"`
	Tool      string     `json:"tool"`
	Digest    string     `json:"digest,omitempty"`
	Platforms []Platform `json:"platforms,omitempty"`
}

func (r Result) PinnedReference() string {
	if !strings.HasPrefix(r.Digest, "sha256:") {
		return r.Reference
	}
	base, _, _ := strings.Cut(r.Reference, "@")
	if slash, colon := strings.LastIndex(base, "/"), strings.LastIndex(base, ":"); colon > slash {
		base = base[:colon]
	}
	return base + "@" + r.Digest
}

func Registry(reference string) (string, error) {
	parsed, err := parseReference(reference)
	if err != nil {
		return "", err
	}
	return parsed.Registry, nil
}

func Repository(reference string) (string, error) {
	parsed, err := parseReference(reference)
	if err != nil {
		return "", err
	}
	return parsed.Registry + "/" + parsed.Repository, nil
}

func (r Result) Supports(osName, architecture string) bool {
	osName = strings.ToLower(strings.TrimSpace(osName))
	architecture = strings.ToLower(strings.TrimSpace(architecture))
	for _, platform := range r.Platforms {
		if strings.EqualFold(platform.OS, osName) && strings.EqualFold(platform.Architecture, architecture) {
			return true
		}
	}
	return false
}

func Inspect(ctx context.Context, reference string) (Result, error) {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return Result{}, fmt.Errorf("image reference is empty")
	}
	commands := []struct {
		tool string
		args []string
	}{
		{tool: "crane", args: []string{"manifest", reference}},
		{tool: "docker", args: []string{"manifest", "inspect", reference}},
	}
	var unavailable []string
	var toolErrs []string
	for _, command := range commands {
		if _, err := exec.LookPath(command.tool); err != nil {
			unavailable = append(unavailable, command.tool)
			continue
		}
		out, err := exec.CommandContext(ctx, command.tool, command.args...).CombinedOutput()
		if err != nil {
			// A present-but-failing CLI (stale binary, auth hiccup, daemon down)
			// must not block the registry fallback; record and keep going.
			toolErrs = append(toolErrs, fmt.Sprintf("%s: %s", command.tool, strings.TrimSpace(string(out))))
			continue
		}
		platforms, err := parsePlatforms(out)
		if err != nil {
			return Result{}, fmt.Errorf("parse %s manifest for %q: %w", command.tool, reference, err)
		}
		return Result{Reference: reference, Tool: command.tool, Platforms: platforms}, nil
	}
	result, err := inspectRegistry(ctx, reference)
	if err != nil {
		if isDockerHubReference(reference) && strings.Contains(err.Error(), "HTTP 429") {
			if fallback, fallbackErr := inspectDockerHubTag(ctx, reference); fallbackErr == nil {
				return fallback, nil
			}
		}
		if len(toolErrs) > 0 {
			return Result{}, fmt.Errorf("inspect image %q: registry fallback failed after local tool errors (%s): %w", reference, strings.Join(toolErrs, "; "), err)
		}
		return Result{}, fmt.Errorf("inspect image %q with registry API after %s unavailable: %w", reference, strings.Join(unavailable, ", "), err)
	}
	return result, nil
}

func isDockerHubReference(value string) bool {
	parsed, err := parseReference(value)
	return err == nil && parsed.Registry == "registry-1.docker.io"
}

func inspectDockerHubTag(ctx context.Context, value string) (Result, error) {
	ref, err := parseReference(value)
	if err != nil {
		return Result{}, err
	}
	if ref.Registry != "registry-1.docker.io" {
		return Result{}, fmt.Errorf("Docker Hub metadata only supports Docker Hub references")
	}
	endpoint := "https://hub.docker.com/v2/repositories/" + ref.Repository + "/tags/" + url.PathEscape(ref.Reference)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Result{}, err
	}
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	if err != nil {
		return Result{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return Result{}, fmt.Errorf("Docker Hub tag metadata returned HTTP %d", response.StatusCode)
	}
	var tag struct {
		Digest string `json:"digest"`
		Images []struct {
			OS           string `json:"os"`
			Architecture string `json:"architecture"`
			Variant      string `json:"variant"`
			Digest       string `json:"digest"`
		} `json:"images"`
	}
	if err := json.NewDecoder(response.Body).Decode(&tag); err != nil {
		return Result{}, err
	}
	platforms := make([]Platform, 0, len(tag.Images))
	for _, item := range tag.Images {
		if item.OS == "" || item.Architecture == "" {
			continue
		}
		platforms = append(platforms, Platform{OS: item.OS, Architecture: item.Architecture, Variant: item.Variant})
		if tag.Digest == "" && item.Digest != "" {
			tag.Digest = item.Digest
		}
	}
	if len(platforms) == 0 {
		return Result{}, fmt.Errorf("Docker Hub tag metadata did not include platforms")
	}
	sort.Slice(platforms, func(i, j int) bool { return platforms[i].String() < platforms[j].String() })
	return Result{Reference: value, Tool: "docker-hub-api", Digest: tag.Digest, Platforms: platforms}, nil
}

func FindCompatible(ctx context.Context, current string, target Platform, limit int) ([]Result, error) {
	if limit <= 0 {
		limit = 3
	}
	parsed, err := parseReference(current)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 10 * time.Second}
	endpoint := parsed.baseURL() + "/v2/" + parsed.Repository + "/tags/list?n=50"
	body, _, err := registryGet(ctx, client, endpoint, parsed.Repository)
	if err != nil {
		return nil, err
	}
	var response struct {
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}
	sort.Strings(response.Tags)
	results := []Result{}
	for _, tag := range response.Tags {
		if tag == "" || tag == parsed.Reference {
			continue
		}
		candidate := parsed.withTag(tag)
		result, err := inspectRegistry(ctx, candidate)
		if err != nil || !result.Supports(target.OS, target.Architecture) {
			continue
		}
		results = append(results, result)
		if len(results) >= limit {
			break
		}
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("no compatible tag found in the first %d repository tags", len(response.Tags))
	}
	return results, nil
}

func parsePlatforms(data []byte) ([]Platform, error) {
	var manifest struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
		Variant      string `json:"variant"`
		Manifests    []struct {
			Platform Platform `json:"platform"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}
	platforms := []Platform{}
	if manifest.OS != "" || manifest.Architecture != "" {
		platforms = append(platforms, Platform{OS: manifest.OS, Architecture: manifest.Architecture, Variant: manifest.Variant})
	}
	for _, entry := range manifest.Manifests {
		if entry.Platform.OS == "" && entry.Platform.Architecture == "" {
			continue
		}
		platforms = append(platforms, entry.Platform)
	}
	if len(platforms) == 0 {
		return nil, fmt.Errorf("manifest did not contain platform information")
	}
	sort.Slice(platforms, func(i, j int) bool { return platforms[i].String() < platforms[j].String() })
	return platforms, nil
}

func inspectRegistry(ctx context.Context, reference string) (Result, error) {
	ref, err := parseReference(reference)
	if err != nil {
		return Result{}, err
	}
	client := &http.Client{Timeout: 15 * time.Second}
	manifestURL := ref.baseURL() + "/v2/" + ref.Repository + "/manifests/" + url.PathEscape(ref.Reference)
	body, headers, err := registryGet(ctx, client, manifestURL, ref.Repository)
	if err != nil {
		return Result{}, err
	}
	platforms, err := parsePlatforms(body)
	if err == nil {
		return Result{Reference: reference, Tool: "registry-api", Digest: headers.Get("Docker-Content-Digest"), Platforms: platforms}, nil
	}
	var manifest struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
	}
	if unmarshalErr := json.Unmarshal(body, &manifest); unmarshalErr != nil || manifest.Config.Digest == "" {
		return Result{}, fmt.Errorf("manifest has no platform index or config descriptor (%s)", headers.Get("Content-Type"))
	}
	configURL := ref.baseURL() + "/v2/" + ref.Repository + "/blobs/" + url.PathEscape(manifest.Config.Digest)
	config, _, err := registryGet(ctx, client, configURL, ref.Repository)
	if err != nil {
		return Result{}, err
	}
	platforms, err = parsePlatforms(config)
	if err != nil {
		return Result{}, fmt.Errorf("parse image config platform: %w", err)
	}
	return Result{Reference: reference, Tool: "registry-api", Digest: headers.Get("Docker-Content-Digest"), Platforms: platforms}, nil
}

type reference struct {
	Registry   string
	Repository string
	Reference  string
}

func parseReference(value string) (reference, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, " \t\n") {
		return reference{}, fmt.Errorf("invalid image reference")
	}
	name, refValue, hasRef := strings.Cut(value, "@")
	// Strip an optional :tag from the name even when the reference is
	// digest-pinned (e.g. ghcr.io/acme/api:v1@sha256:...), so the tag never
	// leaks into Repository. The digest wins as the reference when present.
	lastSlash := strings.LastIndex(name, "/")
	if colon := strings.LastIndex(name, ":"); colon > lastSlash {
		if !hasRef {
			refValue = name[colon+1:]
		}
		name = name[:colon]
	}
	if refValue == "" {
		refValue = "latest"
	}
	parts := strings.Split(name, "/")
	registry := "registry-1.docker.io"
	repository := name
	if len(parts) > 1 && (strings.Contains(parts[0], ".") || strings.Contains(parts[0], ":") || parts[0] == "localhost") {
		registry = parts[0]
		repository = strings.Join(parts[1:], "/")
	} else if len(parts) == 1 {
		repository = "library/" + name
	}
	if repository == "" {
		return reference{}, fmt.Errorf("invalid image repository")
	}
	return reference{Registry: registry, Repository: repository, Reference: refValue}, nil
}

func (r reference) baseURL() string {
	return "https://" + r.Registry
}

func (r reference) withTag(tag string) string {
	prefix := r.Registry + "/" + r.Repository
	if r.Registry == "registry-1.docker.io" {
		prefix = r.Repository
	}
	return prefix + ":" + tag
}

func registryGet(ctx context.Context, client *http.Client, endpoint, repository string) ([]byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json",
	}, ", "))
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err := readResponse(resp)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return nil, nil, fmt.Errorf("registry returned HTTP %d", resp.StatusCode)
		}
		return body, resp.Header, nil
	}
	token, err := bearerToken(ctx, client, resp.Header.Get("WWW-Authenticate"), repository)
	if err != nil {
		return nil, nil, err
	}
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json")
	resp, err = client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err = readResponse(resp)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, nil, fmt.Errorf("registry returned HTTP %d after authentication", resp.StatusCode)
	}
	return body, resp.Header, nil
}

func bearerToken(ctx context.Context, client *http.Client, header, repository string) (string, error) {
	if !strings.HasPrefix(strings.ToLower(header), "bearer ") {
		return "", fmt.Errorf("registry requires unsupported authentication")
	}
	params := map[string]string{}
	for _, item := range strings.Split(strings.TrimSpace(header[len("Bearer "):]), ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(item), "=")
		if !ok {
			continue
		}
		params[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), "\"")
	}
	realm := params["realm"]
	if realm == "" {
		return "", fmt.Errorf("registry bearer challenge did not include a realm")
	}
	u, err := url.Parse(realm)
	if err != nil {
		return "", err
	}
	query := u.Query()
	if params["service"] != "" {
		query.Set("service", params["service"])
	}
	if params["scope"] != "" {
		query.Set("scope", params["scope"])
	} else {
		query.Set("scope", "repository:"+repository+":pull")
	}
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := readResponse(resp)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("registry token service returned HTTP %d", resp.StatusCode)
	}
	var token struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &token); err != nil {
		return "", err
	}
	if token.Token != "" {
		return token.Token, nil
	}
	if token.AccessToken != "" {
		return token.AccessToken, nil
	}
	return "", fmt.Errorf("registry token response was empty")
}

func readResponse(resp *http.Response) ([]byte, error) {
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}

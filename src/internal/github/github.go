package github

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-xray-sdk-go/xray"
	"github.com/google/go-github/v54/github"
	"github.com/shurcooL/githubv4"
	"golang.org/x/exp/slog"
)

// GHRepository encapsulates GitHub repository details with a focus on its releases.
// This is structured to align with the expected response format from GitHub's GraphQL API.
type GHRepository struct {
	Repository struct {
		Releases struct {
			PageInfo struct {
				HasNextPage bool   // Indicates if there are more pages of releases.
				EndCursor   string // The cursor for pagination.
			}
			Nodes []GHRelease // A list of GitHub releases.
		} `graphql:"releases(first: $perPage, orderBy: {field: CREATED_AT, direction: DESC}, after: $endCursor)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

// GHRelease represents a release on GitHub.
// This provides details about the release, including its tag name, release assets, and its release status (draft, prerelease, etc.).
type GHRelease struct {
	ID            string // The ID of the release.
	TagName       string // The tag name associated with the release.
	ReleaseAssets struct {
		Nodes []ReleaseAsset // A list of assets for the release.
	} `graphql:"releaseAssets(first:100)"`
	IsDraft      bool     // Indicates if the release is a draft.
	IsLatest     bool     // Indicates if the release is the latest.
	IsPrerelease bool     // Indicates if the release is a prerelease.
	TagCommit    struct { // The commit associated with the release tag.
		//nolint: revive, stylecheck // This is a struct provided by the GitHub GraphQL API.
		TarballUrl string // The URL to download the release tarball.
	}
	CreatedAt time.Time // The time the release was created.
}

// ReleaseAsset represents a single asset within a GitHub release.
// This includes details such as the download URL and the name of the asset.
type ReleaseAsset struct {
	ID          string // The ID of the asset.
	DownloadURL string // The URL to download the asset.
	Name        string // The name of the asset.
}

func RepositoryExists(ctx context.Context, managedGhClient *github.Client, namespace, name string) (exists bool, err error) {
	err = xray.Capture(ctx, "github.repository.exists", func(tracedCtx context.Context) error {
		xray.AddAnnotation(tracedCtx, "namespace", namespace)
		xray.AddAnnotation(tracedCtx, "name", name)

		slog.Info("Checking if repository exists")

		_, response, getErr := managedGhClient.Repositories.Get(tracedCtx, namespace, name)
		if getErr != nil {
			if response.StatusCode == http.StatusNotFound {
				slog.Info("Repository does not exist")
				return nil
			}
			slog.Error("Failed to get repository", "error", getErr)
			return fmt.Errorf("failed to get repository: %w", getErr)
		}

		slog.Info("Repository exists")
		exists = true
		return nil
	})

	return exists, err
}

func FindRelease(ctx context.Context, ghClient *githubv4.Client, namespace, name, versionNumber string) (release *GHRelease, err error) {
	err = xray.Capture(ctx, "github.release.find", func(tracedCtx context.Context) error {
		xray.AddAnnotation(tracedCtx, "namespace", namespace)
		xray.AddAnnotation(tracedCtx, "name", name)
		xray.AddAnnotation(tracedCtx, "versionNumber", versionNumber)

		variables := initVariables(namespace, name)

		slog.Info("Finding release")

		for {
			nodes, endCursor, fetchErr := fetchReleaseNodes(tracedCtx, ghClient, variables)
			if fetchErr != nil {
				slog.Error("Failed to fetch release nodes", "error", fetchErr)
				return fmt.Errorf("failed to fetch release nodes: %w", fetchErr)
			}

			for _, r := range nodes {
				if r.IsDraft || r.IsPrerelease {
					continue
				}

				if r.TagName == fmt.Sprintf("v%s", versionNumber) {
					rCopy := r
					release = &rCopy
					return nil
				}
			}

			if endCursor == nil {
				break
			}
			variables["endCursor"] = githubv4.String(*endCursor)
		}

		return nil
	})

	if release == nil {
		slog.Info("Release not found")
		return nil, err
	}

	slog.Info("Release found", "release", release)
	return release, err
}

const sincePadding = 2 * time.Minute

func FetchReleases(ctx context.Context, ghClient *githubv4.Client, namespace, name string, since *time.Time) (releases []GHRelease, err error) {
	err = xray.Capture(ctx, "github.releases.fetch", func(tracedCtx context.Context) error {
		xray.AddAnnotation(tracedCtx, "namespace", namespace)
		xray.AddAnnotation(tracedCtx, "name", name)

		variables := initVariables(namespace, name)

		slog.Info("Fetching new releases", "since", since)

		for {
			nodes, endCursor, fetchErr := fetchReleaseNodes(tracedCtx, ghClient, variables)
			if fetchErr != nil {
				slog.Error("Failed to fetch release nodes", "error", fetchErr)
				return fmt.Errorf("failed to fetch release nodes: %w", fetchErr)
			}

			slog.Info("Checking for possible new releases", "count", len(nodes))

			for _, r := range nodes {
				if r.IsDraft || r.IsPrerelease {
					continue
				}

				// if we have been provided a "since" time, we should only fetch releases created after that time
				// if the release was created before the given time, we can stop fetching
				// this is because all releases are ordered by creation date
				if since != nil && r.CreatedAt.Before(since.Add(-sincePadding)) {
					slog.Info("New release was created before given time, stopping reading releases", "release", r.TagName, "created_at", r.CreatedAt, "since", since)
					break
				}

				slog.Info("New release fetched", "release", r.TagName, "created_at", r.CreatedAt)
				releases = append(releases, r)
			}

			if endCursor == nil {
				slog.Info("No more releases to fetch")
				break
			}

			variables["endCursor"] = githubv4.String(*endCursor)
		}

		return nil
	})

	slog.Info("New releases fetched", "count", len(releases))
	return releases, err
}

func initVariables(namespace, name string) map[string]interface{} {
	perPage := 100 // TODO: make this configurable
	return map[string]interface{}{
		"owner":     githubv4.String(namespace),
		"name":      githubv4.String(name),
		"perPage":   githubv4.Int(perPage),
		"endCursor": (*githubv4.String)(nil),
	}
}

// fetchReleaseNodes will fetch a page of releases from the github api and return the nodes, endCursor, and an error
// endCursor will be nil if there are no more pages
func fetchReleaseNodes(ctx context.Context, ghClient *githubv4.Client, variables map[string]interface{}) (releases []GHRelease, endCursor *string, err error) {
	err = xray.Capture(ctx, "github.releases.nodes", func(tracedCtx context.Context) error {
		var query GHRepository

		if queryErr := ghClient.Query(tracedCtx, &query, variables); queryErr != nil {
			return fmt.Errorf("failed to query for releases: %w", queryErr)
		}

		if query.Repository.Releases.PageInfo.HasNextPage {
			endCursor = &query.Repository.Releases.PageInfo.EndCursor
		}

		releases = query.Repository.Releases.Nodes

		return nil
	})

	return releases, endCursor, err
}

func FindAssetBySuffix(assets []ReleaseAsset, suffix string) *ReleaseAsset {
	slog.Info("Finding asset by suffix", "suffix", suffix)
	for _, asset := range assets {
		if strings.HasSuffix(asset.Name, suffix) {
			slog.Info("Asset found", "asset", asset)
			return &asset
		}
	}
	slog.Info("Asset not found")
	return nil
}

const githubAssetDownloadTimeout = 60 * time.Second

func DownloadAssetContents(ctx context.Context, downloadURL string) (body io.ReadCloser, err error) {
	httpClient := xray.Client(&http.Client{Timeout: githubAssetDownloadTimeout})

	err = xray.Capture(ctx, "github.asset.download", func(tracedCtx context.Context) error {
		slog.Info("Downloading asset", "url", downloadURL)
		req, reqErr := http.NewRequestWithContext(tracedCtx, http.MethodGet, downloadURL, nil)
		if reqErr != nil {
			slog.Error("Failed to create request", "error", reqErr)
			return fmt.Errorf("failed to create request: %w", reqErr)
		}

		resp, respErr := httpClient.Do(req)
		if respErr != nil {
			slog.Error("Error downloading asset", "error", respErr)
			return fmt.Errorf("error downloading asset: %w", respErr)
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			slog.Error("Unexpected status code when downloading asset", "status_code", resp.StatusCode)
			return fmt.Errorf("unexpected status code when downloading asset: %d", resp.StatusCode)
		}

		body = resp.Body

		return nil
	})

	slog.Info("Asset downloaded successfully")
	return body, err
}

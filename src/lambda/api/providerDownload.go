package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/opentofu/registry/internal/config"
	"github.com/opentofu/registry/internal/providers/providercache"
	"golang.org/x/exp/slog"

	"github.com/aws/aws-lambda-go/events"

	"github.com/opentofu/registry/internal/github"
	"github.com/opentofu/registry/internal/providers"
)

type DownloadHandlerPathParams struct {
	Architecture string `json:"arch"`
	OS           string `json:"os"`
	Namespace    string `json:"namespace"`
	Type         string `json:"type"`
	Version      string `json:"version"`
}

func (p DownloadHandlerPathParams) AnnotateLogger() {
	logger := slog.Default()
	logger = logger.
		With("namespace", p.Namespace).
		With("type", p.Type).
		With("version", p.Version).
		With("os", p.OS).
		With("arch", p.Architecture)
	slog.SetDefault(logger)
}

func getDownloadPathParams(req events.APIGatewayProxyRequest) DownloadHandlerPathParams {
	return DownloadHandlerPathParams{
		Architecture: req.PathParameters["arch"],
		OS:           req.PathParameters["os"],
		Namespace:    req.PathParameters["namespace"],
		Type:         req.PathParameters["type"],
		Version:      req.PathParameters["version"],
	}
}

func downloadProviderVersion(config config.Config) LambdaFunc {
	return func(ctx context.Context, req events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
		params := getDownloadPathParams(req)
		params.AnnotateLogger()
		effectiveNamespace := config.EffectiveProviderNamespace(params.Namespace)

		// Construct the repo name.
		repoName := providers.GetRepoName(params.Type)

		// For now, we will ignore errors from the cache and just fetch from GH instead
		document, _ := config.ProviderVersionCache.GetItem(ctx, fmt.Sprintf("%s/%s", effectiveNamespace, params.Type))
		if document != nil {
			return processDocumentForProviderDownload(document, effectiveNamespace, params)
		}

		// check the repo exists
		exists, err := github.RepositoryExists(ctx, config.ManagedGithubClient, effectiveNamespace, repoName)
		if err != nil {
			slog.Error("Error checking if repo exists", "error", err)
			return events.APIGatewayProxyResponse{StatusCode: http.StatusInternalServerError}, err
		}
		if !exists {
			slog.Info("Repo does not exist")
			return NotFoundResponse, nil
		}
		
		versionDownloadResponse, err := providers.GetVersion(ctx, config.RawGithubv4Client, effectiveNamespace, repoName, params.Version, params.OS, params.Architecture)
		if err != nil {
			// log the error too for dev
			slog.Error("Error getting version", "error", err)
			return events.APIGatewayProxyResponse{StatusCode: http.StatusInternalServerError}, err
		}

		resBody, err := json.Marshal(versionDownloadResponse)
		if err != nil {
			return events.APIGatewayProxyResponse{StatusCode: http.StatusInternalServerError}, err
		}
		return events.APIGatewayProxyResponse{StatusCode: http.StatusOK, Body: string(resBody)}, nil
	}
}

func processDocumentForProviderDownload(document *providercache.VersionListingItem, effectiveNamespace string, params DownloadHandlerPathParams) (events.APIGatewayProxyResponse, error) {
	slog.Info("Found document in cache", "last_updated", document.LastUpdated, "versions", len(document.Versions))

	// try and find the version in the document
	versionDetails := document.GetVersionDetails(params.Version, params.OS, params.Architecture)
	if versionDetails != nil {

		// attach the signing keys
		publicKeys, keysErr := providers.KeysForNamespace(effectiveNamespace)
		if keysErr != nil {
			slog.Error("Could not get public keys", "error", keysErr)
			return events.APIGatewayProxyResponse{StatusCode: http.StatusInternalServerError}, keysErr
		}

		keys := providers.SigningKeys{}
		keys.GPGPublicKeys = publicKeys

		versionDetails.SigningKeys = keys

		slog.Info("Found version in document", "version", params.Version)
		resBody, err := json.Marshal(versionDetails)
		if err != nil {
			return events.APIGatewayProxyResponse{StatusCode: http.StatusInternalServerError}, err
		}
		return events.APIGatewayProxyResponse{StatusCode: http.StatusOK, Body: string(resBody)}, nil
	}
	slog.Info("Version not found in document, returning 404", "version", params.Version)
	return NotFoundResponse, nil
}

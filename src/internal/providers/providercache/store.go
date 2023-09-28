package providercache

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/opentofu/registry/internal/providers/types"
	"golang.org/x/exp/slog"
)

func (p *Handler) Store(ctx context.Context, key string, versions types.VersionList) error {
	item := types.CacheItem{
		Provider:    key,
		Versions:    versions,
		LastUpdated: time.Now(),
	}

	marshalledItem, err := attributevalue.MarshalMap(item)
	if err != nil {
		slog.Error("got error marshalling dynamodb item", "error", err)
		return fmt.Errorf("got error marshalling dynamodb item: %w", err)
	}

	putItemInput := &dynamodb.PutItemInput{
		Item:      marshalledItem,
		TableName: p.TableName,
	}

	slog.Info("Storing provider versions", "key", key, "versions", len(versions))
	_, err = p.Client.PutItem(ctx, putItemInput)
	if err != nil {
		slog.Error("got error calling PutItem", "error", err)
		return fmt.Errorf("got error calling PutItem: %w", err)
	}

	slog.Info("Successfully stored provider versions", "key", key, "versions", len(versions))
	return nil
}

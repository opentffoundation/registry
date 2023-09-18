package main

import (
	"context"
	"fmt"
	"github.com/aws/aws-lambda-go/lambda"
)

type PopulateProviderVersionsEvent struct {
	Namespace string `json:"name"`
	Type      string `json:"type"`
}

func HandleRequest(_ context.Context, name PopulateProviderVersionsEvent) (string, error) {
	return fmt.Sprintf("Fetching %s/%s", name.Namespace, name.Type), nil
}

func main() {
	lambda.Start(HandleRequest)
}

package dazsecrets

import (
	"context"
	"crypto/rand"
	"time"
)

// Client invokes one provider process per operation.
type Client struct{ config ClientConfig }

// NewClient validates and returns an explicitly configured client.
func NewClient(config ClientConfig) (*Client, error) {
	if err := validateClientConfig(config); err != nil {
		return nil, err
	}
	return &Client{config: config}, nil
}

// NewDefaultClient loads and validates the sole default configuration file.
func NewDefaultClient() (*Client, error) {
	config, err := LoadDefaultConfig()
	if err != nil {
		return nil, err
	}
	return NewClient(config)
}

// Info queries provider identity and negotiated protocol version.
func (c *Client) Info(ctx context.Context) (Info, error) {
	result, err := c.call(ctx, request{Operation: "info"})
	return Info{ProviderID: result.ProviderID, Major: result.Major, Minor: result.Minor}, err
}

// Get retrieves exact bytes and an opaque revision.
func (c *Client) Get(ctx context.Context, service, account string) (Secret, error) {
	result, err := c.itemCall(ctx, request{Operation: "get", Service: service, Account: account})
	if err != nil {
		return Secret{}, err
	}
	return Secret{Value: cloneBytes(*result.Value), Revision: result.Revision}, nil
}

// Set stores exact bytes. expectedRevision nil means unconditional replacement.
func (c *Client) Set(ctx context.Context, service, account string, value []byte, expectedRevision *string) (Mutation, error) {
	if len(value) > maxValue || invalidRevision(expectedRevision) {
		return Mutation{}, &Error{Code: CodeInvalid}
	}
	storedValue := cloneBytes(value)
	result, err := c.itemCall(ctx, request{Operation: "set", Service: service, Account: account, Value: &storedValue, ExpectedRevision: expectedRevision})
	return Mutation{Revision: result.Revision}, err
}

// Delete deletes an item, optionally requiring an expected revision.
func (c *Client) Delete(ctx context.Context, service, account string, expectedRevision *string) (Deletion, error) {
	if invalidRevision(expectedRevision) {
		return Deletion{}, &Error{Code: CodeInvalid}
	}
	result, err := c.itemCall(ctx, request{Operation: "delete", Service: service, Account: account, ExpectedRevision: expectedRevision})
	return Deletion{Deleted: result.Deleted}, err
}

// ListMetadata returns nonsecret item metadata.
func (c *Client) ListMetadata(ctx context.Context) ([]Metadata, error) {
	result, err := c.call(ctx, request{Operation: "list_metadata"})
	if err != nil {
		return nil, err
	}
	return *result.Metadata, nil
}

func (c *Client) itemCall(ctx context.Context, req request) (response, error) {
	if !validateName(req.Service) || !validateName(req.Account) {
		return response{}, &Error{Code: CodeInvalid}
	}
	return c.call(ctx, req)
}

func (c *Client) call(ctx context.Context, req request) (response, error) {
	operationCtx, cancel := operationContext(ctx, c.config.Timeout)
	defer cancel()
	req.ID = make([]byte, 16)
	if _, err := rand.Read(req.ID); err != nil {
		return response{}, &Error{Code: CodeInternal}
	}
	req.MinMinor, req.MaxMinor = protocolMinor, protocolMinor
	result, err := c.invoke(operationCtx, c.config.ProviderPath, c.config.ProviderID, req)
	if err == nil || c.config.FallbackProviderPath == "" || !fallbackEligible(err) {
		return result, err
	}
	return c.invoke(operationCtx, c.config.FallbackProviderPath, c.config.FallbackProviderID, req)
}

func fallbackEligible(err error) bool {
	return IsCode(err, CodeUnavailable) || IsCode(err, CodeUnsupported)
}

func operationContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, timeout)
}

func invalidRevision(revision *string) bool {
	return revision != nil && !validateRevision(*revision)
}

func cloneBytes(value []byte) []byte {
	cloned := make([]byte, len(value))
	copy(cloned, value)
	return cloned
}

package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/yourorg/scheduler-simulator/pkg/scheduler/types"
	"github.com/yourorg/scheduler-simulator/pkg/simulator/state"
)

// Client is the HTTP client for the simulator API
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a new simulator client
func NewClient(url string) *Client {
	return &Client{
		baseURL:    url,
		httpClient: &http.Client{},
	}
}

// GetState fetches the current cluster state
func (c *Client) GetState(ifNoneMatch string) (*state.ClusterState, string, error) {
	req, err := http.NewRequest("GET", fmt.Sprintf("%s/v1/state", c.baseURL), nil)
	if err != nil {
		return nil, "", err
	}

	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		return nil, ifNoneMatch, nil // No change
	}

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("API returned status %d", resp.StatusCode)
	}

	var state state.ClusterState
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		return nil, "", err
	}

	etag := resp.Header.Get("ETag")
	return &state, etag, nil
}

// SubmitPlacement submits a placement decision
func (c *Client) SubmitPlacement(decision *types.PlacementDecision) (*types.ApplyPlacementResult, error) {
	body, err := json.Marshal(decision)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", fmt.Sprintf("%s/v1/placement", c.baseURL), bytes.NewBuffer(body))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var result types.ApplyPlacementResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	return &result, nil
}

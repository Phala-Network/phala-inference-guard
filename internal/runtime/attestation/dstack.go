package attestation

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

type Dstack interface {
	GetQuote(ctx context.Context, reportData []byte) (QuoteResponse, error)
	Info(ctx context.Context) (map[string]any, error)
}

type QuoteResponse struct {
	Quote      string `json:"quote"`
	EventLog   string `json:"event_log"`
	ReportData string `json:"report_data,omitempty"`
	VMConfig   string `json:"vm_config,omitempty"`
}

type DstackClient struct {
	baseURL string
	client  *http.Client
}

func NewDstackClient(endpoint string, timeout time.Duration) *DstackClient {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		endpoint = strings.TrimSpace(os.Getenv("DSTACK_SIMULATOR_ENDPOINT"))
	}
	if endpoint == "" {
		endpoint = "/var/run/dstack.sock"
	}
	if strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://") {
		return &DstackClient{
			baseURL: strings.TrimRight(endpoint, "/"),
			client:  &http.Client{Timeout: timeout},
		}
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			dialer := net.Dialer{Timeout: timeout}
			return dialer.DialContext(ctx, "unix", endpoint)
		},
		DisableKeepAlives: true,
	}
	return &DstackClient{
		baseURL: "http://dstack",
		client:  &http.Client{Transport: transport, Timeout: timeout},
	}
}

func (c *DstackClient) GetQuote(ctx context.Context, reportData []byte) (QuoteResponse, error) {
	if len(reportData) == 0 {
		return QuoteResponse{}, fmt.Errorf("report_data can not be empty")
	}
	if len(reportData) > 64 {
		return QuoteResponse{}, fmt.Errorf("report_data must be less than 64 bytes")
	}
	var response QuoteResponse
	err := c.postRPC(ctx, "GetQuote", map[string]any{"report_data": hex.EncodeToString(reportData)}, &response)
	return response, err
}

func (c *DstackClient) Info(ctx context.Context) (map[string]any, error) {
	var response map[string]any
	if err := c.postRPC(ctx, "Info", map[string]any{}, &response); err != nil {
		return nil, err
	}
	normalizeInfoResponse(response)
	return response, nil
}

func (c *DstackClient) postRPC(ctx context.Context, method string, payload any, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/"+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "phala-inference-guard/dstack-go")
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("%s status %d", method, response.StatusCode)
	}
	return json.NewDecoder(response.Body).Decode(out)
}

func normalizeInfoResponse(info map[string]any) {
	rawTCB, ok := info["tcb_info"].(string)
	if !ok || strings.TrimSpace(rawTCB) == "" {
		return
	}
	var parsed any
	if err := json.Unmarshal([]byte(rawTCB), &parsed); err == nil {
		info["tcb_info"] = parsed
	}
}

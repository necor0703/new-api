package aws

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/claude"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/gin-gonic/gin"
)

// mantleTarget holds the shared result of resolving a Mantle channel's
// credentials and base URL before an API-specific path is appended.
type mantleTarget struct {
	endpoint *url.URL
	region   string
}

func resolveMantleTarget(info *relaycommon.RelayInfo) (*mantleTarget, error) {
	parts := strings.Split(info.ApiKey, "|")
	if len(parts) != 2 && len(parts) != 3 {
		return nil, fmt.Errorf("invalid AWS credentials: use APIKey|Region or AccessKey|SecretAccessKey|Region")
	}
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			return nil, fmt.Errorf("invalid AWS credentials: empty credential or region")
		}
	}
	region := parts[len(parts)-1]
	for _, ch := range region {
		if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '-' {
			return nil, fmt.Errorf("invalid AWS region")
		}
	}
	baseURL := strings.TrimRight(info.ChannelBaseUrl, "/")
	if baseURL == "" {
		baseURL = "https://bedrock-mantle." + region + ".api.aws"
	}
	endpoint, err := url.Parse(baseURL)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, fmt.Errorf("AWS Mantle base URL must be an HTTPS URL without credentials, query, or fragment")
	}
	return &mantleTarget{endpoint: endpoint, region: region}, nil
}

const mantleAnthropicMessagesPath = "/anthropic/v1/messages"

func mantleRequestURL(info *relaycommon.RelayInfo) (string, error) {
	target, err := resolveMantleTarget(info)
	if err != nil {
		return "", err
	}
	endpoint := target.endpoint
	switch {
	case strings.HasSuffix(endpoint.Path, mantleAnthropicMessagesPath):
	case strings.HasSuffix(endpoint.Path, "/anthropic/v1"):
		endpoint.Path += "/messages"
	case strings.HasSuffix(endpoint.Path, "/anthropic"):
		endpoint.Path += "/v1/messages"
	default:
		endpoint.Path += mantleAnthropicMessagesPath
	}
	return endpoint.String(), nil
}

const mantleOpenaiChatCompletionsPath = "/openai/v1/chat/completions"

// mantleOpenaiRequestURL resolves the Bedrock OpenAI-compatible Chat
// Completions endpoint for the channel's Mantle base URL.
func mantleOpenaiRequestURL(info *relaycommon.RelayInfo) (string, error) {
	target, err := resolveMantleTarget(info)
	if err != nil {
		return "", err
	}
	endpoint := target.endpoint
	switch {
	case strings.HasSuffix(endpoint.Path, mantleOpenaiChatCompletionsPath):
	case strings.HasSuffix(endpoint.Path, "/openai/v1"):
		endpoint.Path += "/chat/completions"
	case strings.HasSuffix(endpoint.Path, "/openai"):
		endpoint.Path += "/v1/chat/completions"
	default:
		endpoint.Path += mantleOpenaiChatCompletionsPath
	}
	return endpoint.String(), nil
}

// mantleStripAndSign removes hop-by-hop headers (which intermediaries may
// drop, breaking SigV4 validation) and signs with SigV4 when the channel
// uses AccessKey/SecretAccessKey credentials. It must run after all other
// headers are final.
func mantleStripAndSign(req *http.Request, info *relaycommon.RelayInfo, signedBody []byte) error {
	parts := strings.Split(info.ApiKey, "|")
	// HTTP/2 and intermediaries can remove hop-by-hop headers. Strip them
	// after overrides, including fields named by Connection, so SigV4 covers
	// only headers that will reach Mantle. Proxy credentials must stay local.
	for _, connection := range req.Header.Values("Connection") {
		for name := range strings.SplitSeq(connection, ",") {
			req.Header.Del(strings.TrimSpace(name))
		}
	}
	for _, name := range []string{
		"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
		"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
	} {
		req.Header.Del(name)
	}
	if len(parts) == 3 {
		// Never send an unrelated bearer credential alongside SigV4. The AWS
		// signer handles timestamps and signing the exact transmitted payload.
		req.Header.Del("Authorization")
		req.Header.Del("x-api-key")
		credentials := aws.Credentials{AccessKeyID: parts[0], SecretAccessKey: parts[1]}
		hash := sha256.Sum256(signedBody)
		if err := v4.NewSigner().SignHTTP(req.Context(), credentials, req, hex.EncodeToString(hash[:]), "bedrock-mantle", parts[2], time.Now()); err != nil {
			return fmt.Errorf("sign AWS Mantle request: %w", err)
		}
	}
	return nil
}

// resolveMantleHeaderOverrides applies administrator header overrides after
// all base headers are set, so overrides win.
func resolveMantleHeaderOverrides(req *http.Request, info *relaycommon.RelayInfo, c *gin.Context) error {
	headerOverride, err := channel.ResolveHeaderOverride(info, c)
	if err != nil {
		return err
	}
	for key, value := range headerOverride {
		req.Header.Set(key, value)
		if strings.EqualFold(key, "Host") {
			req.Host = value
		}
	}
	return nil
}

// decodeMantlePayload decodes the relayed request body into a raw JSON
// object that preserves fields the adaptor does not know about. Callers
// stamp the resolved model name and marshal the final body themselves, so
// any field removed after decoding stays out of the transmitted payload.
func decodeMantlePayload(requestBody io.Reader) (map[string]json.RawMessage, error) {
	var payload map[string]json.RawMessage
	if err := common.DecodeJson(requestBody, &payload); err != nil {
		return nil, fmt.Errorf("decode AWS Mantle request: %w", err)
	}
	if payload == nil {
		return nil, fmt.Errorf("AWS Mantle request must be a JSON object")
	}
	return payload, nil
}

// Mantle uses native Messages JSON and SSE, not the InvokeModel envelope.
// Sign the final payload and headers, including administrator overrides.
func (a *Adaptor) newMantleRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (*http.Request, error) {
	endpoint, err := mantleRequestURL(info)
	if err != nil {
		return nil, err
	}
	if a.IsNova || isNovaModel(getAwsModelID(info.UpstreamModelName)) {
		return nil, fmt.Errorf("AWS Mantle Messages API requires a Claude model; use InvokeModel for Amazon Nova")
	}
	// Use the channel's resolved model even for passthrough requests. Do not
	// apply InvokeModel's version suffixes or automatic cross-region prefixes.
	model := info.UpstreamModelName
	if model == "claude-haiku-4-5-20251001" {
		model = "claude-haiku-4-5"
	}
	if strings.HasPrefix(model, "claude-") {
		model = "anthropic." + model
	}
	payload, err := decodeMantlePayload(requestBody)
	if err != nil {
		return nil, err
	}
	payload["model"], err = common.Marshal(model)
	if err != nil {
		return nil, err
	}
	delete(payload, "anthropic_version")
	delete(payload, "anthropic_beta")
	body, err := common.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal AWS Mantle request: %w", err)
	}
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create AWS Mantle request: %w", err)
	}
	channel.SetupApiRequestHeader(info, c, &req.Header)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	claude.CommonClaudeHeadersOperation(c, &req.Header, info)
	parts := strings.Split(info.ApiKey, "|")
	if len(parts) == 2 {
		req.Header.Set("x-api-key", parts[0])
	}
	if err := resolveMantleHeaderOverrides(req, info, c); err != nil {
		return nil, err
	}
	if err := mantleStripAndSign(req, info, body); err != nil {
		return nil, err
	}
	return req, nil
}

// newMantleOpenaiRequest builds a SigV4-signed Chat Completions request for
// the Bedrock OpenAI-compatible endpoint. Request bodies pass through in
// OpenAI format; no Anthropic conversion is applied.
func (a *Adaptor) newMantleOpenaiRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (*http.Request, error) {
	endpoint, err := mantleOpenaiRequestURL(info)
	if err != nil {
		return nil, err
	}
	if a.IsNova || isNovaModel(info.UpstreamModelName) {
		return nil, fmt.Errorf("AWS Mantle OpenAI endpoint does not serve Amazon Nova models")
	}
	payload, err := decodeMantlePayload(requestBody)
	if err != nil {
		return nil, err
	}
	payload["model"], err = common.Marshal(info.UpstreamModelName)
	if err != nil {
		return nil, err
	}
	// Stray Anthropic-only fields have no meaning for Chat Completions and
	// could be rejected upstream; mirror the Messages path's cleanup.
	delete(payload, "anthropic_version")
	delete(payload, "anthropic_beta")
	body, err := common.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal AWS Mantle request: %w", err)
	}
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create AWS Mantle OpenAI request: %w", err)
	}
	channel.SetupApiRequestHeader(info, c, &req.Header)
	req.Header.Set("Content-Type", "application/json")
	parts := strings.Split(info.ApiKey, "|")
	if len(parts) == 2 {
		req.Header.Set("Authorization", "Bearer "+parts[0])
	}
	if err := resolveMantleHeaderOverrides(req, info, c); err != nil {
		return nil, err
	}
	// Never send an unrelated bearer credential alongside SigV4; the signer
	// detects Authorization/x-api-key and replaces them with computed values.
	if err := mantleStripAndSign(req, info, body); err != nil {
		return nil, err
	}
	return req, nil
}

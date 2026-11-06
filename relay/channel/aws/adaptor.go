package aws

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/claude"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/pkg/errors"

	"github.com/gin-gonic/gin"
)

type ClientMode int

const (
	ClientModeApiKey ClientMode = iota + 1
	ClientModeAKSK
)

type Adaptor struct {
	ClientMode ClientMode
	AwsClient  *bedrockruntime.Client
	AwsModelId string
	AwsReq     any
	IsNova     bool
}

func (a *Adaptor) ConvertGeminiRequest(*gin.Context, *relaycommon.RelayInfo, *dto.GeminiChatRequest) (any, error) {
	//TODO implement me
	return nil, errors.New("not implemented")
}

func (a *Adaptor) ConvertClaudeRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.ClaudeRequest) (any, error) {
	// The Bedrock OpenAI-compatible endpoint only speaks OpenAI formats;
	// translate Anthropic Messages requests before forwarding.
	if info.ChannelOtherSettings.AwsApiType == dto.AwsApiTypeMantleOpenai {
		openaiReq, err := service.ClaudeToOpenAIRequest(*request, info)
		if err != nil {
			return nil, errors.Wrap(err, "failed to convert claude request to openai request")
		}
		openaiReq.Model = info.UpstreamModelName
		return openaiReq, nil
	}
	// Strip empty-shell thinking blocks that some clients (e.g. ZCode)
	// replay in assistant history: {"type":"thinking","thinking":"","signature":""}.
	// Bedrock rejects those with "each thinking block must contain thinking";
	// full thinking blocks and redacted_thinking blocks are left untouched.
	stripEmptyThinkingBlocks(request)
	claudeAdaptor := claude.Adaptor{}
	if _, err := claudeAdaptor.ConvertClaudeRequest(c, info, request); err != nil {
		return nil, err
	}
	for i, message := range request.Messages {
		updated := false
		if !message.IsStringContent() {
			content, err := message.ParseContent()
			if err != nil {
				return nil, errors.Wrap(err, "failed to parse message content")
			}
			for i2, mediaMessage := range content {
				if mediaMessage.Source != nil {
					if mediaMessage.Source.Type == "url" {
						// 使用统一的文件服务获取图片数据
						source := types.NewURLFileSource(mediaMessage.Source.Url)
						base64Data, mimeType, err := service.GetBase64Data(c, source, "formatting image for Claude")
						if err != nil {
							return nil, fmt.Errorf("get file base64 from url failed: %s", err.Error())
						}
						mediaMessage.Source.MediaType = mimeType
						mediaMessage.Source.Data = base64Data
						mediaMessage.Source.Url = ""
						mediaMessage.Source.Type = "base64"
						content[i2] = mediaMessage
						updated = true
					}
				}
			}
			if updated {
				message.SetContent(content)
			}
		}
		if updated {
			request.Messages[i] = message
		}
	}
	return request, nil
}

// stripEmptyThinkingBlocks removes empty-shell thinking blocks that some
// clients (e.g. ZCode) replay in assistant history:
// {"type":"thinking","thinking":"","signature":""}.
// Bedrock rejects those with "each thinking block must contain thinking".
// Blocks with actual content, or with a non-empty signature, are preserved.
// A message is only rewritten when at least one other block survives, so we
// never emit an assistant message with empty content.
func stripEmptyThinkingBlocks(request *dto.ClaudeRequest) {
	for i, message := range request.Messages {
		if message.Role != "assistant" {
			continue
		}
		blocks, ok := message.Content.([]any)
		if !ok {
			continue
		}
		kept := make([]any, 0, len(blocks))
		dropped := false
		for _, block := range blocks {
			blockMap, ok := block.(map[string]any)
			if !ok {
				kept = append(kept, block)
				continue
			}
			if blockType, _ := blockMap["type"].(string); blockType == "thinking" {
				thinkingText, _ := blockMap["thinking"].(string)
				signature, _ := blockMap["signature"].(string)
				if thinkingText == "" && signature == "" {
					dropped = true
					continue
				}
			}
			kept = append(kept, block)
		}
		if dropped && len(kept) > 0 {
			request.Messages[i].Content = kept
		}
	}
}

func (a *Adaptor) ConvertAudioRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.AudioRequest) (io.Reader, error) {
	//TODO implement me
	return nil, errors.New("not implemented")
}

func (a *Adaptor) ConvertImageRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.ImageRequest) (any, error) {
	//TODO implement me
	return nil, errors.New("not implemented")
}

func (a *Adaptor) Init(info *relaycommon.RelayInfo) {
}

func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	if info.ChannelOtherSettings.AwsApiType == dto.AwsApiTypeMantleOpenai {
		return mantleOpenaiRequestURL(info)
	}
	if info.ChannelOtherSettings.AwsApiType == dto.AwsApiTypeMantle {
		return mantleRequestURL(info)
	}
	if info.ChannelOtherSettings.AwsKeyType == dto.AwsKeyTypeApiKey {
		awsModelId := getAwsModelID(info.UpstreamModelName)
		a.ClientMode = ClientModeApiKey
		awsSecret := strings.Split(info.ApiKey, "|")
		if len(awsSecret) != 2 {
			return "", errors.New("invalid aws api key, should be in format of <api-key>|<region>")
		}
		return fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com/model/%s/converse", awsModelId, awsSecret[1]), nil
	} else {
		a.ClientMode = ClientModeAKSK
		return "", nil
	}
}

func (a *Adaptor) SetupRequestHeader(c *gin.Context, req *http.Header, info *relaycommon.RelayInfo) error {
	claude.CommonClaudeHeadersOperation(c, req, info)
	if a.ClientMode == ClientModeApiKey {
		req.Set("Authorization", "Bearer "+info.ApiKey)
	}
	return nil
}

func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	if request == nil {
		return nil, errors.New("request is nil")
	}
	// 检查是否为Nova模型
	if isNovaModel(request.Model) {
		novaReq := convertToNovaRequest(request)
		a.IsNova = true
		return novaReq, nil
	}

	// Bedrock OpenAI-compatible endpoint speaks Chat Completions natively;
	// passthrough the OpenAI request instead of converting to Claude format.
	if info.ChannelOtherSettings.AwsApiType == dto.AwsApiTypeMantleOpenai {
		request.Model = info.UpstreamModelName
		return request, nil
	}

	// 原有的Claude模型处理逻辑
	result, err := service.ConvertRequest(c, info, types.RelayFormatClaude, request)
	if err != nil {
		return nil, errors.Wrap(err, "failed to convert openai request to claude request")
	}
	claudeReq, ok := result.Value.(*dto.ClaudeRequest)
	if !ok {
		return nil, fmt.Errorf("expected Anthropic Messages request, got %T", result.Value)
	}
	info.UpstreamModelName = claudeReq.Model
	return claudeReq, err
}

func (a *Adaptor) ConvertRerankRequest(c *gin.Context, relayMode int, request dto.RerankRequest) (any, error) {
	return nil, nil
}

func (a *Adaptor) ConvertEmbeddingRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.EmbeddingRequest) (any, error) {
	//TODO implement me
	return nil, errors.New("not implemented")
}

func (a *Adaptor) ConvertOpenAIResponsesRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.OpenAIResponsesRequest) (any, error) {
	// The Bedrock OpenAI-compatible endpoint supports the Responses API;
	// reuse the standard OpenAI adaptor conversion (reasoning effort etc.)
	// and pass the request through.
	openaiAdaptor := openai.Adaptor{}
	converted, err := openaiAdaptor.ConvertOpenAIResponsesRequest(c, info, request)
	if err != nil {
		return nil, err
	}
	if req, ok := converted.(dto.OpenAIResponsesRequest); ok {
		req.Model = info.UpstreamModelName
		return req, nil
	}
	return converted, nil
}

func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {
	switch info.ChannelOtherSettings.AwsApiType {
	case dto.AwsApiTypeMantle:
		req, err := a.newMantleRequest(c, info, requestBody)
		if err != nil {
			return nil, err
		}
		resp, err := channel.DoRequest(c, req, info)
		if err != nil {
			return nil, newAwsInvokeError(c.Request.Context(), err, "Mantle Messages")
		}
		return resp, nil
	case dto.AwsApiTypeMantleOpenai:
		req, err := a.newMantleOpenaiRequest(c, info, requestBody)
		if err != nil {
			return nil, err
		}
		resp, err := channel.DoRequest(c, req, info)
		if err != nil {
			return nil, newAwsInvokeError(c.Request.Context(), err, "Mantle Chat Completions")
		}
		return resp, nil
	case "", dto.AwsApiTypeInvokeModel:
		// Unconfigured channels retain the existing InvokeModel behavior.
	default:
		return nil, errors.New("invalid AWS API type")
	}
	if a.ClientMode == ClientModeApiKey {
		return channel.DoApiRequest(a, c, info, requestBody)
	} else {
		return doAwsClientRequest(c, info, a, requestBody)
	}
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (usage any, err *types.NewAPIError) {
	// Bedrock OpenAI-compatible endpoint returns OpenAI-format responses.
	if info.ChannelOtherSettings.AwsApiType == dto.AwsApiTypeMantleOpenai {
		openaiAdaptor := openai.Adaptor{}
		return openaiAdaptor.DoResponse(c, resp, info)
	}
	if info.ChannelOtherSettings.AwsApiType == dto.AwsApiTypeMantle || a.ClientMode == ClientModeApiKey {
		claudeAdaptor := claude.Adaptor{}
		usage, err = claudeAdaptor.DoResponse(c, resp, info)
	} else {
		if a.IsNova {
			err, usage = handleNovaRequest(c, info, a)
		} else {
			if info.IsStream {
				err, usage = awsStreamHandler(c, info, a)
			} else {
				err, usage = awsHandler(c, info, a)
			}
		}
	}
	return
}

func (a *Adaptor) GetModelList() (models []string) {
	for n := range awsModelIDMap {
		models = append(models, n)
	}

	return
}

func (a *Adaptor) GetChannelName() string {
	return ChannelName
}

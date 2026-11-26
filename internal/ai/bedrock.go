package ai

import (
	"context"
	"errors"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/aws/smithy-go"
	"github.com/fixora/kubectl-fixora/internal/analyzer"
)

type BedrockConverseAPI interface {
	Converse(context.Context, *bedrockruntime.ConverseInput, ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error)
}

type bedrockConverseAdapter struct{ client BedrockConverseAPI }

// NewBedrockConverseAdapter accepts a service client for tests. Pass nil to use
// the AWS SDK's normal credential chain and AWS_REGION configuration.
func NewBedrockConverseAdapter(client BedrockConverseAPI) Adapter {
	return bedrockConverseAdapter{client: client}
}

func (a bedrockConverseAdapter) Explain(ctx context.Context, request Request) (*analyzer.AIResult, error) {
	if request.Provider.Protocol != ProtocolBedrockConverse {
		return nil, &ProviderError{Kind: ErrorUnsupported}
	}
	client := a.client
	if client == nil {
		region := strings.TrimSpace(request.Provider.EndpointOptions["region"])
		if region == "" {
			return nil, &ProviderError{Kind: ErrorConfiguration}
		}
		cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
		if err != nil {
			return nil, &ProviderError{Kind: ErrorConfiguration}
		}
		client = bedrockruntime.NewFromConfig(cfg)
	}
	input := &bedrockruntime.ConverseInput{
		ModelId:  aws.String(request.Model),
		System:   []types.SystemContentBlock{&types.SystemContentBlockMemberText{Value: request.SystemPrompt}},
		Messages: []types.Message{{Role: types.ConversationRoleUser, Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: request.UserPrompt}}}},
	}
	response, err := client.Converse(ctx, input)
	if err != nil {
		return nil, normalizeBedrockError(err)
	}
	if response == nil {
		return nil, &ProviderError{Kind: ErrorIncomplete}
	}
	switch response.StopReason {
	case types.StopReasonEndTurn, types.StopReasonStopSequence:
	case types.StopReasonMaxTokens:
		return nil, &ProviderError{Kind: ErrorIncomplete}
	case types.StopReasonGuardrailIntervened, types.StopReasonContentFiltered:
		return nil, &ProviderError{Kind: ErrorRefusal}
	default:
		return nil, &ProviderError{Kind: ErrorIncomplete}
	}
	message, ok := response.Output.(*types.ConverseOutputMemberMessage)
	if !ok {
		return nil, &ProviderError{Kind: ErrorIncomplete}
	}
	var answer strings.Builder
	for _, block := range message.Value.Content {
		if text, ok := block.(*types.ContentBlockMemberText); ok {
			answer.WriteString(text.Value)
		}
	}
	return ValidateAIResult([]byte(answer.String()))
}

func normalizeBedrockError(err error) error {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		kind := ErrorProvider
		switch apiErr.ErrorCode() {
		case "ThrottlingException", "TooManyRequestsException":
			kind = ErrorRateLimit
		case "ServiceUnavailableException", "InternalServerException", "ModelNotReadyException":
			kind = ErrorTransient
		case "AccessDeniedException", "UnrecognizedClientException":
			kind = ErrorAuthentication
		case "ValidationException", "ResourceNotFoundException":
			kind = ErrorUnsupported
		}
		return &ProviderError{Kind: kind, Code: safeDetail(apiErr.ErrorCode())}
	}
	return &ProviderError{Kind: ErrorProvider}
}

package yandexaikit

import (
	"context"
	"encoding/base64"
	"fmt"
)

const imageGenerationAsyncPath = "/foundationModels/v1/imageGenerationAsync"

// imagePromptLimit — лимит промпта YandexART (research §9: 500 символов).
const imagePromptLimit = 500

// ImageInput — нативный вход YandexART. Model — bare-id ("yandex-art") ИЛИ
// полный modelUri ("art://<folder>/yandex-art/latest").
type ImageInput struct {
	Model       string         // "" → yandex-art
	Messages    []ImageMessage // {text, weight}; weight<0 = negative prompt (не поддержан)
	Seed        *int64
	MimeType    string // только "image/jpeg" документирован
	AspectRatio *AspectRatio
}

// ImageResult — декодированный кадр + Usage (Images=1) + cost (RUB per-image).
type ImageResult struct {
	Image        []byte // декодировано из base64
	MimeType     string
	ModelVersion string
	Usage        Usage
	Cost         RUBMicros
}

// ImageGenerationAsync submit'ит YandexART-генерацию (всегда async LRO). Возвращает
// operation-id; результат — через AwaitImage или PollOperation.
func (c *Client) ImageGenerationAsync(ctx context.Context, in ImageInput) (string, error) {
	for _, m := range in.Messages {
		if len([]rune(m.Text)) > imagePromptLimit {
			return "", fmt.Errorf("%w: image prompt exceeds %d chars", ErrInvalidRequest, imagePromptLimit)
		}
	}
	model := in.Model
	if model == "" {
		model = "yandex-art"
	}
	body := ImageGenerationRequest{
		ModelURI: c.modelURI("art", model),
		Messages: in.Messages,
		GenerationOptions: ImageGenerationOptions{
			MimeType:    in.MimeType,
			Seed:        in.Seed,
			AspectRatio: in.AspectRatio,
		},
	}
	var op Operation
	if err := c.doJSON(ctx, jsonRequest{
		method:   "POST",
		url:      c.cfg.BaseURL + imageGenerationAsyncPath,
		body:     body,
		endpoint: "image-generation-async",
		model:    model,
	}, &op); err != nil {
		return "", err
	}
	return op.ID, nil
}

// AwaitImage поллит операцию imageGenerationAsync до done, декодит base64-кадр.
// Cost метерится по yandex-art-ключу (per-image, Images=1).
func (c *Client) AwaitImage(ctx context.Context, operationID string) (*ImageResult, error) {
	op, err := c.awaitOperation(ctx, operationID)
	if err != nil {
		return nil, err
	}
	var payload ImageGenerationResponse
	if err := decodeOperationResponse(op, &payload); err != nil {
		return nil, err
	}
	img, err := base64.StdEncoding.DecodeString(payload.Image)
	if err != nil {
		return nil, fmt.Errorf("yandexaikit: decode image base64: %w", err)
	}
	u := Usage{Images: 1}
	cost := c.costOf("yandex-art", u)
	c.instr.recordUsage(ctx, "yandex-art", u, cost)
	return &ImageResult{
		Image:        img,
		MimeType:     "image/jpeg",
		ModelVersion: payload.ModelVersion,
		Usage:        u,
		Cost:         cost,
	}, nil
}

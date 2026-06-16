package googleaikit

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/genai"
)

// pollInterval — пауза между опросами Veo-операции при блокирующем WaitVideo.
const pollInterval = 10 * time.Second

// VideoRequest — нативный вход генерации видео (Veo). Image (image-to-video) и
// прочие knobs опциональны.
type VideoRequest struct {
	Model           string // "" → Config.DefaultModel
	Prompt          string
	Image           *MediaBlock // image-to-video (inlineData или fileData)
	AspectRatio     string      // 16:9 | 9:16
	DurationSeconds *int32      // 4 | 6 | 8
	Resolution      string      // 720p | 1080p | 4k
	NumberOfVideos  int32
}

// GenVideo — одно сгенерированное видео (URI у провайдера; скачивание — через Raw()).
type GenVideo struct {
	URI      string
	MIMEType string
}

// VideoOperation — хэндл long-running Veo-операции. Done==false → ещё в работе;
// после Done заполнены Videos (успех) или Err (провал). Op хранит SDK-операцию
// для повторного поллинга.
type VideoOperation struct {
	Name   string
	Done   bool
	Videos []GenVideo
	Usage  Usage
	Cost   USDMicros
	Err    error

	op      *genai.GenerateVideosOperation //nolint:unused // SDK-хэндл для PollVideo
	model   string
	durSecs *int32 // заявленная длительность (для per-second биллинга на done)
}

// SubmitVideo запускает асинхронную генерацию (predictLongRunning) и возвращает
// незавершённую операцию; результат забирают PollVideo/WaitVideo.
func (c *Client) SubmitVideo(ctx context.Context, req VideoRequest) (*VideoOperation, error) {
	model := c.modelOr(req.Model)
	cfg := &genai.GenerateVideosConfig{
		NumberOfVideos:  req.NumberOfVideos,
		AspectRatio:     req.AspectRatio,
		Resolution:      req.Resolution,
		DurationSeconds: req.DurationSeconds,
	}
	var img *genai.Image
	if req.Image != nil {
		img = &genai.Image{MIMEType: req.Image.MIMEType, ImageBytes: req.Image.Data, GCSURI: req.Image.FileURI}
	}

	start := c.clock.Now()
	op, err := c.sdk.Models.GenerateVideos(ctx, model, req.Prompt, img, cfg)
	if err != nil {
		c.instr.record(ctx, model, "video", "error", c.clock.Now().Sub(start), Usage{}, 0)
		return nil, classify(err)
	}
	return c.wrapVideoOp(op, model, req.DurationSeconds), nil
}

// PollVideo опрашивает операцию один раз (GetVideosOperation). На done заполняет
// Videos/Usage/Cost (длительность → AudioSeconds для per-second биллинга) и метрику.
func (c *Client) PollVideo(ctx context.Context, vop *VideoOperation) (*VideoOperation, error) {
	if vop == nil || vop.op == nil {
		return nil, fmt.Errorf("%w: nil video operation", ErrInvalidRequest)
	}
	op, err := c.sdk.Operations.GetVideosOperation(ctx, vop.op, nil)
	if err != nil {
		return nil, classify(err)
	}
	return c.wrapVideoOp(op, vop.model, vop.durSecs), nil
}

// WaitVideo поллит до завершения с паузами pollInterval (clockkit-friendly: sleep
// уважает ctx). Записывает финальную метрику video-вызова.
func (c *Client) WaitVideo(ctx context.Context, vop *VideoOperation) (*VideoOperation, error) {
	start := c.clock.Now()
	for {
		var err error
		vop, err = c.PollVideo(ctx, vop)
		if err != nil {
			c.instr.record(ctx, vop.model, "video", "error", c.clock.Now().Sub(start), Usage{}, 0)
			return nil, err
		}
		if vop.Done {
			status := "ok"
			if vop.Err != nil {
				status = "error"
			}
			c.instr.record(ctx, vop.model, "video", status, c.clock.Now().Sub(start), vop.Usage, vop.Cost)
			return vop, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

func (c *Client) wrapVideoOp(op *genai.GenerateVideosOperation, model string, dur *int32) *VideoOperation {
	out := &VideoOperation{Name: op.Name, Done: op.Done, op: op, model: model, durSecs: dur}
	if op.Error != nil {
		out.Err = fmt.Errorf("%w: %v", ErrOperation, op.Error)
	}
	if op.Response != nil {
		for _, gv := range op.Response.GeneratedVideos {
			if gv != nil && gv.Video != nil {
				out.Videos = append(out.Videos, GenVideo{URI: gv.Video.URI, MIMEType: gv.Video.MIMEType})
			}
		}
	}
	if op.Done && dur != nil {
		out.Usage.AudioSeconds = float64(*dur)
		out.Cost = c.costOf(model, out.Usage)
	}
	return out
}

package sberaikit

import "context"

// GeneratedImage — одна сгенерированная картинка: file-id из <img src> + байты,
// вытянутые из /files/{id}/content (image-as-file).
type GeneratedImage struct {
	FileID      string
	Bytes       []byte
	ContentType string
}

// ImageResult — результат генерации через text2image: породивший chat-ответ (для
// usage/cost — биллинг идёт по токенам chat-вызова, отдельной per-image строки
// нет, research §8) + скачанные картинки.
type ImageResult struct {
	Chat   *ChatResponse
	Images []GeneratedImage
}

// GenerateImage запускает image-генерацию: chat с function_call="auto" и промптом
// (с глаголом рисования), затем для каждого <img src="file-id"/> в ответе тянет
// байты через FileContent. GigaChat не имеет /images-эндпоинта — генерация это
// built-in функция text2image внутри chat, результат — file-id (research §3).
//
// prompt — пользовательский текст ("Нарисуй розового кота"); model — chat-модель
// (GigaChat-2/-Pro/-Max). Дополнительные сообщения (системный стиль) — через Chat
// напрямую + ImageFileIDs + FileContent, если нужен полный контроль.
func (c *Client) GenerateImage(ctx context.Context, model, prompt string) (*ImageResult, error) {
	resp, err := c.Chat(ctx, ChatRequest{
		Model:        model,
		Messages:     []Message{{Role: RoleUser, Content: prompt}},
		FunctionCall: "auto",
	})
	if err != nil {
		return nil, err
	}

	res := &ImageResult{Chat: resp}
	for _, id := range resp.ImageFileIDs() {
		b, ct, err := c.FileContent(ctx, id)
		if err != nil {
			return res, err
		}
		res.Images = append(res.Images, GeneratedImage{FileID: id, Bytes: b, ContentType: ct})
	}
	return res, nil
}

package amocrmkit

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

// FileLink — ссылка на привязанный файл (GET /files/{uuid}/links).
type FileLink struct {
	FileUUID   string `json:"file_uuid"`
	EntityID   int64  `json:"entity_id"`
	EntityType string `json:"entity_type"`
}

// AttachFileInput — элемент тела PUT /{entity}/{id}/files.
type AttachFileInput struct {
	FileUUID string `json:"file_uuid"`
}

// AttachFiles — PUT /api/v4/{entity_type}/{id}/files (202).
func (c *Client) AttachFiles(ctx context.Context, acc Account, entityType string, entityID int64, in []AttachFileInput) error {
	e, err := entitySegment(entityType)
	if err != nil {
		return err
	}
	return c.sendNoContent(ctx, acc, http.MethodPut, "files.attach", fmt.Sprintf("/api/v4/%s/%d/files", e, entityID), in)
}

// DetachFiles — DELETE /api/v4/{entity_type}/{id}/files (202).
func (c *Client) DetachFiles(ctx context.Context, acc Account, entityType string, entityID int64, in []AttachFileInput) error {
	e, err := entitySegment(entityType)
	if err != nil {
		return err
	}
	return c.sendNoContent(ctx, acc, http.MethodDelete, "files.detach", fmt.Sprintf("/api/v4/%s/%d/files", e, entityID), in)
}

// GetFileLinks — GET /api/v4/files/{uuid}/links.
func (c *Client) GetFileLinks(ctx context.Context, acc Account, fileUUID string) (Page[FileLink], error) {
	return listEntities[FileLink](ctx, c, acc, "files.links", "/api/v4/files/"+seg(fileUUID)+"/links", "links", nil)
}

// Event — событие ленты (GET /events).
type Event struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	EntityID    int64  `json:"entity_id"`
	EntityType  string `json:"entity_type"`
	CreatedBy   int64  `json:"created_by"`
	CreatedAt   int64  `json:"created_at"`
	ValueBefore []any  `json:"value_before"`
	ValueAfter  []any  `json:"value_after"`
	AccountID   int64  `json:"account_id"`
}

// ListEvents — GET /api/v4/events.
func (c *Client) ListEvents(ctx context.Context, acc Account, p ListParams) (Page[Event], error) {
	return listEntities[Event](ctx, c, acc, "events.list", "/api/v4/events", "events", p.values())
}

// File — объект Drive. Промежуточный ответ на часть несёт только NextURL и
// SessionID: файл собран, когда пришёл объект с UUID.
type File struct {
	UUID                string         `json:"uuid"`
	Type                string         `json:"type"`
	Name                string         `json:"name"`
	SanitizedName       string         `json:"sanitized_name"`
	Size                int64          `json:"size"`
	IsTrashed           bool           `json:"is_trashed"`
	HasMultipleVersions bool           `json:"has_multiple_versions"`
	CreatedAt           int64          `json:"created_at"`
	UpdatedAt           int64          `json:"updated_at"`
	Metadata            map[string]any `json:"metadata"`
	Previews            []any          `json:"previews"`
	NextURL             string         `json:"next_url,omitempty"`
	SessionID           string         `json:"session_id,omitempty"`
}

// UploadSession — ответ POST /v1.0/sessions (адрес и шаг для чанковой загрузки).
type UploadSession struct {
	UploadURL   string `json:"upload_url"`
	MaxPartSize int64  `json:"max_part_size"`
	FileUUID    string `json:"file_uuid"`
}

// UploadSessionInput — тело POST /v1.0/sessions.
type UploadSessionInput struct {
	FileName    string `json:"file_name"`
	FileSize    int64  `json:"file_size"`
	ContentType string `json:"content_type,omitempty"`
}

// CreateUploadSession — POST {drive_url}/v1.0/sessions.
func (c *Client) CreateUploadSession(ctx context.Context, acc Account, driveURL string, in UploadSessionInput) (UploadSession, error) {
	var out UploadSession
	err := c.doREST(ctx, restReq{acc: acc, base: driveURL, method: http.MethodPost, path: "/v1.0/sessions", body: in, endpoint: "drive.session.create"}, &out)
	return out, err
}

// UploadPart — POST {upload_url} с частью файла и заголовком Content-Range.
// Возвращает промежуточный ответ (File.NextURL — адрес следующей части) либо
// финальный File. Для файла целиком есть UploadFile.
func (c *Client) UploadPart(ctx context.Context, acc Account, uploadURL, contentRange string, chunk []byte) (File, error) {
	if err := acc.validate(); err != nil {
		return File{}, fmt.Errorf("amocrmkit: drive.upload: %w", err)
	}
	u, err := c.resolveURL(uploadURL, "", nil)
	if err != nil {
		return File{}, fmt.Errorf("amocrmkit: drive.upload: %w", err)
	}
	if err := c.checkExternalHost(u, acc); err != nil {
		return File{}, fmt.Errorf("amocrmkit: drive.upload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(chunk))
	if err != nil {
		return File{}, fmt.Errorf("amocrmkit: drive.upload: %w: new request: %w", ErrInvalidRequest, err)
	}
	req.Header.Set("Authorization", "Bearer "+acc.Token)
	req.Header.Set("Content-Type", "application/octet-stream")
	if contentRange != "" {
		req.Header.Set("Content-Range", contentRange)
	}
	var out File
	err = c.execute(ctx, req, call{endpoint: "drive.upload", acc: acc.ref()}, &out)
	return out, err
}

// UploadFile загружает файл целиком: сессия → части по MaxPartSize из r,
// каждая следующая идёт на next_url предыдущего ответа. В памяти живёт не
// больше одной части; in.FileSize должен совпадать с длиной r.
func (c *Client) UploadFile(ctx context.Context, acc Account, driveURL string, in UploadSessionInput, r io.Reader) (File, error) {
	sess, err := c.CreateUploadSession(ctx, acc, driveURL, in)
	if err != nil {
		return File{}, err
	}
	if sess.MaxPartSize <= 0 {
		return File{}, fmt.Errorf("amocrmkit: drive.upload: %w: session without max_part_size", ErrServer)
	}

	buf := make([]byte, sess.MaxPartSize)
	next, offset := sess.UploadURL, int64(0)
	for next != "" {
		n, readErr := io.ReadFull(r, buf)
		if readErr != nil && readErr != io.ErrUnexpectedEOF && readErr != io.EOF {
			return File{}, fmt.Errorf("amocrmkit: drive.upload: read part: %w", readErr)
		}
		if n == 0 {
			return File{}, fmt.Errorf("amocrmkit: drive.upload: %w: reader shorter than file_size %d", ErrInvalidRequest, in.FileSize)
		}
		rng := "bytes " + strconv.FormatInt(offset, 10) + "-" + strconv.FormatInt(offset+int64(n)-1, 10) + "/" + strconv.FormatInt(in.FileSize, 10)
		out, err := c.UploadPart(ctx, acc, next, rng, buf[:n])
		if err != nil {
			return File{}, err
		}
		offset += int64(n)
		if out.NextURL == "" {
			return out, nil
		}
		next = out.NextURL
	}
	return File{}, fmt.Errorf("amocrmkit: drive.upload: %w: session without upload_url", ErrServer)
}

// PatchFileInput — тело PATCH {drive_url}/v1.0/files/{uuid}.
type PatchFileInput struct {
	Name string `json:"name,omitempty"`
}

// PatchFile — PATCH {drive_url}/v1.0/files/{uuid}.
func (c *Client) PatchFile(ctx context.Context, acc Account, driveURL, fileUUID string, in PatchFileInput) (File, error) {
	var out File
	path := "/v1.0/files/" + seg(fileUUID)
	err := c.doREST(ctx, restReq{acc: acc, base: driveURL, method: http.MethodPatch, path: path, body: in, endpoint: "drive.patch"}, &out)
	return out, err
}

// TrashFiles — DELETE {drive_url}/v1.0/files (batch по uuid в теле).
func (c *Client) TrashFiles(ctx context.Context, acc Account, driveURL string, uuids []string) error {
	body := make([]map[string]string, len(uuids))
	for i, id := range uuids {
		body[i] = map[string]string{"uuid": id}
	}
	return c.doREST(ctx, restReq{acc: acc, base: driveURL, method: http.MethodDelete, path: "/v1.0/files", body: body, endpoint: "drive.trash"}, nil)
}

// RestoreFiles — POST {drive_url}/v1.0/files/restore.
func (c *Client) RestoreFiles(ctx context.Context, acc Account, driveURL string, uuids []string) error {
	body := make([]map[string]string, len(uuids))
	for i, id := range uuids {
		body[i] = map[string]string{"uuid": id}
	}
	return c.doREST(ctx, restReq{acc: acc, base: driveURL, method: http.MethodPost, path: "/v1.0/files/restore", body: body, endpoint: "drive.restore"}, nil)
}

// GetFileVersions — GET {drive_url}/v1.0/files/{uuid}/versions.
func (c *Client) GetFileVersions(ctx context.Context, acc Account, driveURL, fileUUID string) (Page[File], error) {
	var env listEnvelope
	path := "/v1.0/files/" + seg(fileUUID) + "/versions"
	if err := c.doREST(ctx, restReq{acc: acc, base: driveURL, method: http.MethodGet, path: path, endpoint: "drive.versions"}, &env); err != nil {
		return Page[File]{}, err
	}
	return embeddedPage[File](env, "versions")
}

// ListFiles — GET {drive_url}/v1.0/files.
func (c *Client) ListFiles(ctx context.Context, acc Account, driveURL string, p ListParams) (Page[File], error) {
	var env listEnvelope
	if err := c.doREST(ctx, restReq{acc: acc, base: driveURL, method: http.MethodGet, path: "/v1.0/files", query: p.values(), endpoint: "drive.list"}, &env); err != nil {
		return Page[File]{}, err
	}
	return embeddedPage[File](env, "files")
}

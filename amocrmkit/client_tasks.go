package amocrmkit

import (
	"context"
	"fmt"
	"net/http"
)

// Task — объект задачи (GET-ответ).
type Task struct {
	ID                int64       `json:"id"`
	CreatedBy         int64       `json:"created_by"`
	UpdatedBy         int64       `json:"updated_by"`
	CreatedAt         int64       `json:"created_at"`
	UpdatedAt         int64       `json:"updated_at"`
	ResponsibleUserID int64       `json:"responsible_user_id"`
	GroupID           int64       `json:"group_id"`
	EntityID          int64       `json:"entity_id"`
	EntityType        string      `json:"entity_type"`
	IsCompleted       bool        `json:"is_completed"`
	TaskTypeID        int64       `json:"task_type_id"`
	Text              string      `json:"text"`
	Duration          int         `json:"duration"`
	CompleteTill      int64       `json:"complete_till"`
	Result            *TaskResult `json:"result"`
	AccountID         int64       `json:"account_id"`
}

// TaskResult — результат выполнения задачи.
type TaskResult struct {
	Text string `json:"text"`
}

// TaskInput — тело POST/PATCH /tasks.
type TaskInput struct {
	ID                int64  `json:"id,omitempty"`
	ResponsibleUserID int64  `json:"responsible_user_id,omitempty"`
	EntityID          int64  `json:"entity_id,omitempty"`
	EntityType        string `json:"entity_type,omitempty"`
	TaskTypeID        int64  `json:"task_type_id,omitempty"`
	Text              string `json:"text,omitempty"`
	CompleteTill      int64  `json:"complete_till,omitempty"`
	RequestID         string `json:"request_id,omitempty"`
}

// CompleteTaskInput — тело PATCH /tasks/{id} для завершения задачи.
type CompleteTaskInput struct {
	IsCompleted bool        `json:"is_completed"`
	Result      *TaskResult `json:"result,omitempty"`
}

// GetTask — GET /api/v4/tasks/{id}. Пустой ответ — ErrNotFound.
func (c *Client) GetTask(ctx context.Context, acc Account, id int64, with ...string) (Task, error) {
	return getOne[Task](ctx, c, acc, "tasks.get", fmt.Sprintf("/api/v4/tasks/%d", id), withValues(with))
}

// ListTasks — GET /api/v4/tasks.
func (c *Client) ListTasks(ctx context.Context, acc Account, p ListParams) (Page[Task], error) {
	return listEntities[Task](ctx, c, acc, "tasks.list", "/api/v4/tasks", "tasks", p.values())
}

// CreateTasks — POST /api/v4/tasks (batch до 250).
func (c *Client) CreateTasks(ctx context.Context, acc Account, in []TaskInput) ([]CreatedEntity, error) {
	return sendBatch[TaskInput, CreatedEntity](ctx, c, acc, http.MethodPost, "tasks.create", "/api/v4/tasks", "tasks", in)
}

// UpdateTasks — PATCH /api/v4/tasks (batch до 250).
func (c *Client) UpdateTasks(ctx context.Context, acc Account, in []TaskInput) ([]CreatedEntity, error) {
	return sendBatch[TaskInput, CreatedEntity](ctx, c, acc, http.MethodPatch, "tasks.update", "/api/v4/tasks", "tasks", in)
}

// CompleteTask — PATCH /api/v4/tasks/{id} (is_completed + result).
func (c *Client) CompleteTask(ctx context.Context, acc Account, id int64, result string) (Task, error) {
	in := CompleteTaskInput{IsCompleted: true}
	if result != "" {
		in.Result = &TaskResult{Text: result}
	}
	return sendOne[Task](ctx, c, acc, http.MethodPatch, "tasks.complete", fmt.Sprintf("/api/v4/tasks/%d", id), in)
}

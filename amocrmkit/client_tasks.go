package amocrmkit

import (
	"context"
	"fmt"
	"net/http"
)

// tasks.

// Task — объект задачи (GET-ответ).
type Task struct {
	ID                int         `json:"id"`
	CreatedBy         int         `json:"created_by"`
	UpdatedBy         int         `json:"updated_by"`
	CreatedAt         int64       `json:"created_at"`
	UpdatedAt         int64       `json:"updated_at"`
	ResponsibleUserID int         `json:"responsible_user_id"`
	GroupID           int         `json:"group_id"`
	EntityID          int         `json:"entity_id"`
	EntityType        string      `json:"entity_type"`
	IsCompleted       bool        `json:"is_completed"`
	TaskTypeID        int         `json:"task_type_id"`
	Text              string      `json:"text"`
	Duration          int         `json:"duration"`
	CompleteTill      int64       `json:"complete_till"`
	Result            *TaskResult `json:"result"`
	AccountID         int         `json:"account_id"`
}

// TaskResult — результат выполнения задачи.
type TaskResult struct {
	Text string `json:"text"`
}

// TaskInput — тело POST/PATCH /tasks.
type TaskInput struct {
	ID                int    `json:"id,omitempty"`
	ResponsibleUserID int    `json:"responsible_user_id,omitempty"`
	EntityID          int    `json:"entity_id,omitempty"`
	EntityType        string `json:"entity_type,omitempty"`
	TaskTypeID        int    `json:"task_type_id,omitempty"`
	Text              string `json:"text,omitempty"`
	CompleteTill      int64  `json:"complete_till,omitempty"`
	RequestID         string `json:"request_id,omitempty"`
}

// CompleteTaskInput — тело PATCH /tasks/{id} для завершения задачи.
type CompleteTaskInput struct {
	IsCompleted bool        `json:"is_completed"`
	Result      *TaskResult `json:"result,omitempty"`
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
func (c *Client) CompleteTask(ctx context.Context, acc Account, id int, result string) (Task, error) {
	in := CompleteTaskInput{IsCompleted: true}
	if result != "" {
		in.Result = &TaskResult{Text: result}
	}
	return sendOne[Task](ctx, c, acc, http.MethodPatch, "tasks.complete", fmt.Sprintf("/api/v4/tasks/%d", id), in)
}

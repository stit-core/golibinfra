package amocrmkit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// wireCase — контракт одного метода: что уходит на провод (METHOD + path + query
// + тело) и что метод достаёт из ответа.
type wireCase struct {
	name      string
	want      string // "METHOD /path"
	wantQuery string
	status    int
	resp      string
	do        func(ctx context.Context, c *Client) (any, error)
	wantBody  string // JSON тела запроса (сравнение как JSON-значений)
	check     func(t *testing.T, got any)
}

const driveBase = "https://drive-b.amocrm.ru"

func TestWireContracts(t *testing.T) {
	acc := testAccount()
	ctx := context.Background()

	cases := []wireCase{
		// account / meta
		{
			name: "GetAmojoID", want: "GET /api/v4/account", wantQuery: "with=amojo_id",
			resp: `{"id":1,"amojo_id":"uuid-1"}`,
			do:   func(ctx context.Context, c *Client) (any, error) { return c.GetAmojoID(ctx, acc) },
			check: func(t *testing.T, got any) {
				if got.(string) != "uuid-1" {
					t.Errorf("amojo_id = %v", got)
				}
			},
		},
		{
			name: "GetDriveURL", want: "GET /api/v4/account", wantQuery: "with=drive_url",
			resp: `{"id":1,"drive_url":"https://drive-b.amocrm.ru"}`,
			do:   func(ctx context.Context, c *Client) (any, error) { return c.GetDriveURL(ctx, acc) },
			check: func(t *testing.T, got any) {
				if got.(string) != driveBase {
					t.Errorf("drive_url = %v", got)
				}
			},
		},
		{
			name: "GetAccountTaskTypes", want: "GET /api/v4/account", wantQuery: "with=task_types",
			resp: `{"id":1,"_embedded":{"task_types":[{"id":1,"name":"Связаться","code":"FOLLOW_UP"}]}}`,
			do:   func(ctx context.Context, c *Client) (any, error) { return c.GetAccountTaskTypes(ctx, acc) },
			check: func(t *testing.T, got any) {
				types := got.([]TaskType)
				if len(types) != 1 || types[0].Code != "FOLLOW_UP" {
					t.Errorf("task_types = %+v", types)
				}
			},
		},
		{
			name: "GetEventTypes", want: "GET /api/v4/events/types",
			resp: `{"_embedded":{"events_types":[{"type":"lead_added","key":"lead_added","lang":"Сделка создана"}]}}`,
			do:   func(ctx context.Context, c *Client) (any, error) { return c.GetEventTypes(ctx, acc) },
			check: func(t *testing.T, got any) {
				if len(got.([]EventType)) != 1 {
					t.Errorf("event types = %+v", got)
				}
			},
		},

		// leads
		{
			name: "GetLead", want: "GET /api/v4/leads/17", wantQuery: "with=contacts",
			resp: `{"id":17,"name":"Сделка"}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.GetLead(ctx, acc, 17, "contacts")
			},
			check: func(t *testing.T, got any) {
				if got.(Lead).ID != 17 {
					t.Errorf("lead = %+v", got)
				}
			},
		},
		{
			name: "UpdateLeads", want: "PATCH /api/v4/leads",
			resp: `{"_embedded":{"leads":[{"id":9}]}}`, wantBody: `[{"id":9,"name":"n"}]`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.UpdateLeads(ctx, acc, []LeadInput{{ID: 9, Name: "n"}})
			},
		},
		{
			name: "CreateUnsortedSIP", want: "POST /api/v4/leads/unsorted/sip",
			resp: `{"_embedded":{"unsorted":[{"uid":"uid-1","category":"sip"}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.CreateUnsortedSIP(ctx, acc, []UnsortedSIPInput{{SourceUID: "s"}})
			},
		},
		{
			name: "CreateUnsortedForms", want: "POST /api/v4/leads/unsorted/forms",
			resp: `{"_embedded":{"unsorted":[{"uid":"uid-2","category":"forms"}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.CreateUnsortedForms(ctx, acc, []UnsortedFormInput{{SourceUID: "s"}})
			},
		},
		{
			name: "AcceptUnsorted", want: "POST /api/v4/leads/unsorted/uid-1/accept",
			resp: `{"uid":"uid-1"}`, wantBody: `{"user_id":123,"status_id":1}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.AcceptUnsorted(ctx, acc, "uid-1", UnsortedAcceptInput{UserID: 123, StatusID: 1})
			},
		},
		{
			name: "DeclineUnsorted", want: "DELETE /api/v4/leads/unsorted/uid-1/decline", status: http.StatusNoContent,
			do: func(ctx context.Context, c *Client) (any, error) {
				return nil, c.DeclineUnsorted(ctx, acc, "uid-1")
			},
		},
		{
			name: "LinkUnsorted", want: "POST /api/v4/leads/unsorted/uid-1/link", resp: `{"uid":"uid-1"}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.LinkUnsorted(ctx, acc, "uid-1", UnsortedLinkInput{Link: map[string]any{"entity_id": 1}})
			},
		},

		// contacts / companies
		{
			name: "GetContact", want: "GET /api/v4/contacts/5", resp: `{"id":5,"name":"К"}`,
			do: func(ctx context.Context, c *Client) (any, error) { return c.GetContact(ctx, acc, 5) },
		},
		{
			name: "GetCompany", want: "GET /api/v4/companies/6", resp: `{"id":6}`,
			do: func(ctx context.Context, c *Client) (any, error) { return c.GetCompany(ctx, acc, 6) },
		},
		{
			name: "CreateContacts", want: "POST /api/v4/contacts", resp: `{"_embedded":{"contacts":[{"id":1}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.CreateContacts(ctx, acc, []ContactInput{{Name: "К"}})
			},
		},
		{
			name: "UpdateContacts", want: "PATCH /api/v4/contacts", resp: `{"_embedded":{"contacts":[{"id":1}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.UpdateContacts(ctx, acc, []ContactInput{{ID: 1}})
			},
		},
		{
			name: "ListContactChats", want: "GET /api/v4/contacts/chats",
			resp: `{"_embedded":{"chats":[{"contact_id":1,"chat_id":"c-1"}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.ListContactChats(ctx, acc, ListParams{})
			},
			check: func(t *testing.T, got any) {
				page := got.(Page[ContactChat])
				if len(page.Items) != 1 || page.Items[0].ChatID != "c-1" {
					t.Errorf("chats = %+v", page.Items)
				}
			},
		},
		{
			name: "LinkContactChat", want: "POST /api/v4/contacts/chats",
			resp: `{"_embedded":{"chats":[{"contact_id":1,"chat_id":"c-1"}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.LinkContactChat(ctx, acc, []ContactChat{{ContactID: 1, ChatID: "c-1"}})
			},
		},
		{
			name: "ListCompanies", want: "GET /api/v4/companies", resp: `{"_embedded":{"companies":[{"id":1}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.ListCompanies(ctx, acc, ListParams{})
			},
		},
		{
			name: "CreateCompanies", want: "POST /api/v4/companies", resp: `{"_embedded":{"companies":[{"id":1}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.CreateCompanies(ctx, acc, []CompanyInput{{Name: "ООО"}})
			},
		},
		{
			name: "UpdateCompanies", want: "PATCH /api/v4/companies", resp: `{"_embedded":{"companies":[{"id":1}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.UpdateCompanies(ctx, acc, []CompanyInput{{ID: 1}})
			},
		},

		// customers
		{
			name: "GetCustomer", want: "GET /api/v4/customers/3", resp: `{"id":3}`,
			do: func(ctx context.Context, c *Client) (any, error) { return c.GetCustomer(ctx, acc, 3) },
		},
		{
			name: "ListCustomers", want: "GET /api/v4/customers", resp: `{"_embedded":{"customers":[{"id":3}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.ListCustomers(ctx, acc, ListParams{})
			},
		},
		{
			name: "CreateCustomers", want: "POST /api/v4/customers", resp: `{"_embedded":{"customers":[{"id":3}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.CreateCustomers(ctx, acc, []CustomerInput{{Name: "П"}})
			},
		},
		{
			name: "UpdateCustomers", want: "PATCH /api/v4/customers", resp: `{"_embedded":{"customers":[{"id":3}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.UpdateCustomers(ctx, acc, []CustomerInput{{ID: 3}})
			},
		},
		{
			name: "ListTransactions", want: "GET /api/v4/customers/transactions",
			resp: `{"_embedded":{"transactions":[{"id":8,"price":100}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.ListTransactions(ctx, acc, ListParams{})
			},
		},
		{
			name: "CreateTransactions", want: "POST /api/v4/customers/3/transactions",
			resp: `{"_embedded":{"transactions":[{"id":8}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.CreateTransactions(ctx, acc, 3, []TransactionInput{{Price: 100}})
			},
		},
		{
			name: "DeleteTransaction", want: "DELETE /api/v4/customers/transactions/8", status: http.StatusNoContent,
			do: func(ctx context.Context, c *Client) (any, error) { return nil, c.DeleteTransaction(ctx, acc, 8) },
		},
		{
			name: "AdjustBonusPoints", want: "POST /api/v4/customers/3/bonus_points",
			resp: `{"bonus_points":42}`, wantBody: `{"earn":10}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.AdjustBonusPoints(ctx, acc, 3, BonusPointsInput{Earn: 10})
			},
			check: func(t *testing.T, got any) {
				if got.(BonusPointsResult).BonusPoints != 42 {
					t.Errorf("bonus = %+v", got)
				}
			},
		},
		{
			name: "SetCustomersMode", want: "PATCH /api/v4/customers/mode",
			resp: `{"mode":"segments","is_enabled":true}`, wantBody: `{"mode":"segments","is_enabled":true}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.SetCustomersMode(ctx, acc, CustomersMode{Mode: "segments", IsEnabled: true})
			},
		},

		// tasks
		{
			name: "GetTask", want: "GET /api/v4/tasks/9",
			resp: `{"id":9,"entity_id":5,"entity_type":"leads","text":"call"}`,
			do:   func(ctx context.Context, c *Client) (any, error) { return c.GetTask(ctx, acc, 9) },
			check: func(t *testing.T, got any) {
				if task := got.(Task); task.ID != 9 || task.EntityType != "leads" {
					t.Errorf("task = %+v", task)
				}
			},
		},
		{
			name: "ListTasks", want: "GET /api/v4/tasks", resp: `{"_embedded":{"tasks":[{"id":1}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) { return c.ListTasks(ctx, acc, ListParams{}) },
		},
		{
			name: "CreateTasks", want: "POST /api/v4/tasks", resp: `{"_embedded":{"tasks":[{"id":1}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.CreateTasks(ctx, acc, []TaskInput{{Text: "t"}})
			},
		},
		{
			name: "UpdateTasks", want: "PATCH /api/v4/tasks", resp: `{"_embedded":{"tasks":[{"id":1}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.UpdateTasks(ctx, acc, []TaskInput{{ID: 1}})
			},
		},
		{
			name: "CompleteTask", want: "PATCH /api/v4/tasks/4",
			resp: `{"id":4,"is_completed":true}`, wantBody: `{"is_completed":true,"result":{"text":"ok"}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.CompleteTask(ctx, acc, 4, "ok")
			},
			check: func(t *testing.T, got any) {
				if !got.(Task).IsCompleted {
					t.Errorf("task = %+v", got)
				}
			},
		},

		// notes / calls
		{
			name: "AddNotes", want: "POST /api/v4/leads/notes", resp: `{"_embedded":{"notes":[{"id":2}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.AddNotes(ctx, acc, "leads", []NoteInput{{EntityID: 1, NoteType: "common", Params: map[string]any{"text": "x"}}})
			},
		},
		{
			name: "AddCalls", want: "POST /api/v4/calls", resp: `{"_embedded":{"calls":[{"id":3}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.AddCalls(ctx, acc, []CallInput{{Direction: "inbound", Phone: "+70000000000"}})
			},
		},

		// talks
		{
			name: "GetTalk", want: "GET /api/v4/talks/7", resp: `{"talk_id":7,"chat_id":"c"}`,
			do: func(ctx context.Context, c *Client) (any, error) { return c.GetTalk(ctx, acc, 7) },
			check: func(t *testing.T, got any) {
				if got.(Talk).TalkID != 7 {
					t.Errorf("talk = %+v", got)
				}
			},
		},
		{
			name: "ListTalks", want: "GET /api/v4/talks", wantQuery: "filter%5Bcontact_id%5D=9",
			resp: `{"_embedded":{"talks":[{"talk_id":7}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.ListTalks(ctx, acc, 9, ListParams{})
			},
		},
		{
			name: "CloseTalk", want: "POST /api/v4/talks/7/close", status: http.StatusAccepted, wantBody: `{"force_close":true}`,
			do: func(ctx context.Context, c *Client) (any, error) { return nil, c.CloseTalk(ctx, acc, 7, true) },
		},

		// chat templates
		{
			name: "ListChatTemplates", want: "GET /api/v4/chats/templates",
			resp: `{"_embedded":{"chat_templates":[{"id":1,"name":"t"}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.ListChatTemplates(ctx, acc, ListParams{})
			},
		},
		{
			name: "CreateChatTemplates", want: "POST /api/v4/chats/templates",
			resp: `{"_embedded":{"chat_templates":[{"id":1}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.CreateChatTemplates(ctx, acc, []ChatTemplateInput{{Name: "t", Content: "c"}})
			},
		},
		{
			name: "UpdateChatTemplate", want: "PATCH /api/v4/chats/templates/1", resp: `{"id":1}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.UpdateChatTemplate(ctx, acc, 1, ChatTemplateInput{Content: "c"})
			},
		},
		{
			name: "DeleteChatTemplates", want: "DELETE /api/v4/chats/templates", status: http.StatusNoContent,
			wantBody: `[{"id":1},{"id":2}]`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return nil, c.DeleteChatTemplates(ctx, acc, []int64{1, 2})
			},
		},
		{
			name: "SubmitTemplateReview", want: "POST /api/v4/chats/templates/1/review", resp: `{"id":5,"status":"pending"}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.SubmitTemplateReview(ctx, acc, 1, map[string]any{"category": "MARKETING"})
			},
		},
		{
			name: "UpdateTemplateReview", want: "POST /api/v4/chats/templates/1/review/5", resp: `{"id":5,"status":"approved"}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.UpdateTemplateReview(ctx, acc, 1, 5, map[string]any{"status": "approved"})
			},
		},

		// catalogs
		{
			name: "ListCatalogs", want: "GET /api/v4/catalogs", resp: `{"_embedded":{"catalogs":[{"id":1}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) { return c.ListCatalogs(ctx, acc, ListParams{}) },
		},
		{
			name: "CreateCatalogs", want: "POST /api/v4/catalogs", resp: `{"_embedded":{"catalogs":[{"id":1}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.CreateCatalogs(ctx, acc, []CatalogInput{{Name: "к"}})
			},
		},
		{
			name: "UpdateCatalogs", want: "PATCH /api/v4/catalogs", resp: `{"_embedded":{"catalogs":[{"id":1}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.UpdateCatalogs(ctx, acc, []CatalogInput{{ID: 1}})
			},
		},
		{
			name: "ListCatalogElements", want: "GET /api/v4/catalogs/2/elements",
			resp: `{"_embedded":{"elements":[{"id":10,"catalog_id":2}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.ListCatalogElements(ctx, acc, 2, ListParams{})
			},
			check: func(t *testing.T, got any) {
				page := got.(Page[CatalogElement])
				if len(page.Items) != 1 || page.Items[0].ID != 10 {
					t.Errorf("elements = %+v", page.Items)
				}
			},
		},
		{
			name: "CreateCatalogElements", want: "POST /api/v4/catalogs/2/elements",
			resp: `{"_embedded":{"elements":[{"id":10}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.CreateCatalogElements(ctx, acc, 2, []CatalogElementInput{{Name: "e"}})
			},
		},
		{
			name: "UpdateCatalogElements", want: "PATCH /api/v4/catalogs/2/elements",
			resp: `{"_embedded":{"elements":[{"id":10}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.UpdateCatalogElements(ctx, acc, 2, []CatalogElementInput{{ID: 10}})
			},
		},

		// pipelines / statuses
		{
			name: "ListPipelines", want: "GET /api/v4/leads/pipelines", resp: `{"_embedded":{"pipelines":[{"id":1}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) { return c.ListPipelines(ctx, acc) },
		},
		{
			name: "CreatePipelines", want: "POST /api/v4/leads/pipelines", resp: `{"_embedded":{"pipelines":[{"id":1}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.CreatePipelines(ctx, acc, []PipelineInput{{Name: "в"}})
			},
		},
		{
			name: "UpdatePipeline", want: "PATCH /api/v4/leads/pipelines/1", resp: `{"id":1,"name":"в"}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.UpdatePipeline(ctx, acc, 1, PipelineInput{Name: "в"})
			},
		},
		{
			name: "UpsertStatuses", want: "POST /api/v4/leads/pipelines/1/statuses",
			resp: `{"_embedded":{"statuses":[{"id":142,"pipeline_id":1}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.UpsertStatuses(ctx, acc, 1, []StatusInput{{Name: "с"}})
			},
		},

		// custom fields / groups / tags
		{
			name: "ListCustomFields", want: "GET /api/v4/leads/custom_fields",
			resp: `{"_embedded":{"custom_fields":[{"id":1,"code":"PHONE"}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.ListCustomFields(ctx, acc, "leads", ListParams{})
			},
		},
		{
			name: "ListCustomFieldsCatalog", want: "GET /api/v4/catalogs/7/custom_fields",
			resp: `{"_embedded":{"custom_fields":[{"id":2}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.ListCustomFields(ctx, acc, "catalogs/7", ListParams{})
			},
		},
		{
			name: "CreateCustomFields", want: "POST /api/v4/contacts/custom_fields",
			resp: `{"_embedded":{"custom_fields":[{"id":1}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.CreateCustomFields(ctx, acc, "contacts", []CustomFieldInput{{Name: "f", Type: "text"}})
			},
		},
		{
			name: "UpdateCustomFields", want: "PATCH /api/v4/contacts/custom_fields",
			resp: `{"_embedded":{"custom_fields":[{"id":1}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.UpdateCustomFields(ctx, acc, "contacts", []CustomFieldInput{{ID: 1}})
			},
		},
		{
			name: "DeleteCustomField", want: "DELETE /api/v4/leads/custom_fields/1", status: http.StatusNoContent,
			do: func(ctx context.Context, c *Client) (any, error) {
				return nil, c.DeleteCustomField(ctx, acc, "leads", 1)
			},
		},
		{
			name: "ListCustomFieldGroups", want: "GET /api/v4/leads/custom_fields/groups",
			resp: `{"_embedded":{"custom_field_groups":[{"id":"g","name":"G"}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.ListCustomFieldGroups(ctx, acc, "leads")
			},
		},
		{
			name: "CreateCustomFieldGroups", want: "POST /api/v4/leads/custom_fields/groups",
			resp: `{"_embedded":{"custom_field_groups":[{"id":"g"}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.CreateCustomFieldGroups(ctx, acc, "leads", []CustomFieldGroupInput{{Name: "G"}})
			},
		},
		{
			name: "UpdateCustomFieldGroup", want: "PATCH /api/v4/leads/custom_fields/groups/g-1", resp: `{"id":"g-1"}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.UpdateCustomFieldGroup(ctx, acc, "leads", "g-1", CustomFieldGroupInput{Name: "G"})
			},
		},
		{
			name: "DeleteCustomFieldGroup", want: "DELETE /api/v4/leads/custom_fields/groups/g-1", status: http.StatusNoContent,
			do: func(ctx context.Context, c *Client) (any, error) {
				return nil, c.DeleteCustomFieldGroup(ctx, acc, "leads", "g-1")
			},
		},
		{
			name: "ListTags", want: "GET /api/v4/leads/tags", resp: `{"_embedded":{"tags":[{"id":1,"name":"т"}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.ListTags(ctx, acc, "leads", ListParams{})
			},
		},
		{
			name: "CreateTags", want: "POST /api/v4/leads/tags", resp: `{"_embedded":{"tags":[{"id":1}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.CreateTags(ctx, acc, "leads", []TagCreateInput{{Name: "т"}})
			},
		},
		{
			name: "UpdateTags", want: "PATCH /api/v4/leads/tags", resp: `{"_embedded":{"tags":[{"id":1}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.UpdateTags(ctx, acc, "leads", []TagCreateInput{{ID: 1, Name: "т"}})
			},
		},
		{
			name: "ListLossReasons", want: "GET /api/v4/leads/loss_reasons",
			resp: `{"_embedded":{"loss_reasons":[{"id":1,"name":"дорого"}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.ListLossReasons(ctx, acc, ListParams{})
			},
		},

		// sources / website buttons
		{
			name: "ListSources", want: "GET /api/v4/sources", resp: `{"_embedded":{"sources":[{"id":1}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) { return c.ListSources(ctx, acc, ListParams{}) },
		},
		{
			name: "CreateSources", want: "POST /api/v4/sources", resp: `{"_embedded":{"sources":[{"id":1}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.CreateSources(ctx, acc, []SourceInput{{Name: "s", ExternalID: "e"}})
			},
		},
		{
			name: "UpdateSources", want: "PATCH /api/v4/sources", resp: `{"_embedded":{"sources":[{"id":1}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.UpdateSources(ctx, acc, []SourceInput{{ID: 1}})
			},
		},
		{
			name: "DeleteSources", want: "DELETE /api/v4/sources", status: http.StatusNoContent, wantBody: `[{"id":1}]`,
			do: func(ctx context.Context, c *Client) (any, error) { return nil, c.DeleteSources(ctx, acc, []int64{1}) },
		},
		{
			name: "ListWebsiteButtons", want: "GET /api/v4/website_buttons", wantQuery: "with=scripts",
			resp: `{"_page":1,"_embedded":{"website_buttons":[{"source_id":29181,"button_id":401034,"creation_status":"created"}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.ListWebsiteButtons(ctx, acc, ListParams{With: []string{"scripts"}})
			},
			check: func(t *testing.T, got any) {
				page := got.(Page[WebsiteButton])
				if len(page.Items) != 1 || page.Items[0].SourceID != 29181 {
					t.Errorf("buttons = %+v", page.Items)
				}
			},
		},
		{
			name: "GetWebsiteButton", want: "GET /api/v4/website_buttons/29181",
			resp: `{"source_id":29181,"pipeline_id":null,"script":null}`,
			do:   func(ctx context.Context, c *Client) (any, error) { return c.GetWebsiteButton(ctx, acc, 29181) },
			check: func(t *testing.T, got any) {
				b := got.(WebsiteButton)
				if b.PipelineID != nil || b.Script != nil {
					t.Errorf("nullable fields must stay nil: %+v", b)
				}
			},
		},
		{
			name: "CreateWebsiteButton", want: "POST /api/v4/website_buttons", status: http.StatusAccepted,
			resp: `{"source_id":62112321,"trusted_websites":["example.com"]}`, wantBody: `{"pipeline_id":806215,"trusted_websites":["example.com"]}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.CreateWebsiteButton(ctx, acc, WebsiteButtonInput{PipelineID: 806215, TrustedWebsites: []string{"example.com"}})
			},
		},
		{
			name: "UpdateWebsiteButton", want: "PATCH /api/v4/website_buttons/62112321",
			resp: `{"source_id":62112321}`, wantBody: `{"trusted_websites":{"add":["my-website.com"]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.UpdateWebsiteButton(ctx, acc, 62112321, WebsiteButtonUpdate{
					TrustedWebsites: &TrustedWebsitesEdit{Add: []string{"my-website.com"}},
				})
			},
		},
		{
			name: "ConnectOnlineChat", want: "POST /api/v4/website_buttons/62112321/online_chat", status: http.StatusAccepted,
			do: func(ctx context.Context, c *Client) (any, error) {
				return nil, c.ConnectOnlineChat(ctx, acc, 62112321, map[string]any{})
			},
		},

		// users / roles
		{
			name: "ListUsers", want: "GET /api/v4/users", resp: `{"_embedded":{"users":[{"id":1,"email":"a@b"}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) { return c.ListUsers(ctx, acc, ListParams{}) },
		},
		{
			name: "CreateUsers", want: "POST /api/v4/users", resp: `{"_embedded":{"users":[{"id":1}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.CreateUsers(ctx, acc, []UserInput{{Email: "a@b"}})
			},
		},
		{
			name: "ActivateUsers", want: "POST /api/v4/users/activate", status: http.StatusAccepted, wantBody: `[{"id":123123}]`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return nil, c.ActivateUsers(ctx, acc, []int64{123123})
			},
		},
		{
			name: "DeactivateUsers", want: "POST /api/v4/users/deactivate", status: http.StatusNoContent, wantBody: `[{"id":123123}]`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return nil, c.DeactivateUsers(ctx, acc, []int64{123123})
			},
		},
		{
			name: "ListRoles", want: "GET /api/v4/roles", resp: `{"_embedded":{"roles":[{"id":1}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) { return c.ListRoles(ctx, acc, ListParams{}) },
		},
		{
			name: "CreateRoles", want: "POST /api/v4/roles", resp: `{"_embedded":{"roles":[{"id":1}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.CreateRoles(ctx, acc, []RoleInput{{Name: "r"}})
			},
		},
		{
			name: "UpdateRole", want: "PATCH /api/v4/roles/1", resp: `{"id":1}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.UpdateRole(ctx, acc, 1, RoleInput{Name: "r"})
			},
		},
		{
			name: "DeleteRole", want: "DELETE /api/v4/roles/1", status: http.StatusNoContent,
			do: func(ctx context.Context, c *Client) (any, error) { return nil, c.DeleteRole(ctx, acc, 1) },
		},

		// customer statuses / segments
		{
			name: "ListCustomerStatuses", want: "GET /api/v4/customers/statuses",
			resp: `{"_embedded":{"statuses":[{"id":1,"name":"с"}]}}`,
			do:   func(ctx context.Context, c *Client) (any, error) { return c.ListCustomerStatuses(ctx, acc) },
			check: func(t *testing.T, got any) {
				page := got.(Page[CustomerStatus])
				if len(page.Items) != 1 {
					t.Errorf("statuses = %+v", page.Items)
				}
			},
		},
		{
			name: "CreateCustomerStatuses", want: "POST /api/v4/customers/statuses",
			resp: `{"_embedded":{"statuses":[{"id":1}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.CreateCustomerStatuses(ctx, acc, []CustomerStatusInput{{Name: "с"}})
			},
		},
		{
			name: "UpdateCustomerStatus", want: "PATCH /api/v4/customers/statuses/1", resp: `{"id":1}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.UpdateCustomerStatus(ctx, acc, 1, CustomerStatusInput{Name: "с"})
			},
		},
		{
			name: "DeleteCustomerStatus", want: "DELETE /api/v4/customers/statuses/1", status: http.StatusNoContent,
			do: func(ctx context.Context, c *Client) (any, error) { return nil, c.DeleteCustomerStatus(ctx, acc, 1) },
		},
		{
			name: "ListSegments", want: "GET /api/v4/customers/segments", resp: `{"_embedded":{"segments":[{"id":1}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) { return c.ListSegments(ctx, acc, ListParams{}) },
		},
		{
			name: "CreateSegments", want: "POST /api/v4/customers/segments", resp: `{"_embedded":{"segments":[{"id":1}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.CreateSegments(ctx, acc, []SegmentInput{{Name: "s"}})
			},
		},
		{
			name: "UpdateSegment", want: "PATCH /api/v4/customers/segments/1", resp: `{"id":1}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.UpdateSegment(ctx, acc, 1, SegmentInput{Name: "s"})
			},
		},

		// files / events
		{
			name: "AttachFiles", want: "PUT /api/v4/leads/1/files", status: http.StatusAccepted,
			wantBody: `[{"file_uuid":"u-1"}]`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return nil, c.AttachFiles(ctx, acc, "leads", 1, []AttachFileInput{{FileUUID: "u-1"}})
			},
		},
		{
			name: "DetachFiles", want: "DELETE /api/v4/leads/1/files", status: http.StatusAccepted,
			do: func(ctx context.Context, c *Client) (any, error) {
				return nil, c.DetachFiles(ctx, acc, "leads", 1, []AttachFileInput{{FileUUID: "u-1"}})
			},
		},
		{
			name: "GetFileLinks", want: "GET /api/v4/files/u-1/links",
			resp: `{"_embedded":{"links":[{"file_uuid":"u-1","entity_id":1,"entity_type":"leads"}]}}`,
			do:   func(ctx context.Context, c *Client) (any, error) { return c.GetFileLinks(ctx, acc, "u-1") },
		},
		{
			name: "ListEvents", want: "GET /api/v4/events", resp: `{"_embedded":{"events":[{"id":"e","type":"lead_added"}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) { return c.ListEvents(ctx, acc, ListParams{}) },
		},

		// drive
		{
			name: "CreateUploadSession", want: "POST /v1.0/sessions",
			resp: `{"upload_url":"https://drive-b.amocrm.ru/v1.0/sessions/s-1","max_part_size":1024,"file_uuid":"u-1"}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.CreateUploadSession(ctx, acc, driveBase, UploadSessionInput{FileName: "f.txt", FileSize: 10})
			},
			check: func(t *testing.T, got any) {
				if got.(UploadSession).MaxPartSize != 1024 {
					t.Errorf("session = %+v", got)
				}
			},
		},
		{
			name: "PatchFile", want: "PATCH /v1.0/files/u-1", resp: `{"uuid":"u-1","name":"new"}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.PatchFile(ctx, acc, driveBase, "u-1", PatchFileInput{Name: "new"})
			},
		},
		{
			name: "TrashFiles", want: "DELETE /v1.0/files", status: http.StatusNoContent, wantBody: `[{"uuid":"u-1"}]`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return nil, c.TrashFiles(ctx, acc, driveBase, []string{"u-1"})
			},
		},
		{
			name: "RestoreFiles", want: "POST /v1.0/files/restore", status: http.StatusNoContent,
			do: func(ctx context.Context, c *Client) (any, error) {
				return nil, c.RestoreFiles(ctx, acc, driveBase, []string{"u-1"})
			},
		},
		{
			name: "GetFileVersions", want: "GET /v1.0/files/u-1/versions", resp: `{"_embedded":{"versions":[{"uuid":"u-1"}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.GetFileVersions(ctx, acc, driveBase, "u-1")
			},
		},
		{
			name: "ListFiles", want: "GET /v1.0/files", resp: `{"_embedded":{"files":[{"uuid":"u-1"}]}}`,
			do: func(ctx context.Context, c *Client) (any, error) {
				return c.ListFiles(ctx, acc, driveBase, ListParams{})
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var (
				gotLine  string
				gotQuery string
				gotBody  []byte
			)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotLine = r.Method + " " + r.URL.Path
				gotQuery = r.URL.RawQuery
				gotBody, _ = io.ReadAll(r.Body)
				if tc.status != 0 {
					w.WriteHeader(tc.status)
				}
				_, _ = w.Write([]byte(tc.resp))
			}))
			defer srv.Close()

			c := newTestClient(t, srv.URL)
			got, err := tc.do(ctx, c)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if gotLine != tc.want {
				t.Errorf("wire = %q, want %q", gotLine, tc.want)
			}
			if tc.wantQuery != "" && gotQuery != tc.wantQuery {
				t.Errorf("query = %q, want %q", gotQuery, tc.wantQuery)
			}
			if tc.wantBody != "" {
				assertJSONEqual(t, gotBody, tc.wantBody)
			}
			if tc.check != nil {
				tc.check(t, got)
			}
		})
	}
}

func assertJSONEqual(t *testing.T, got []byte, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("request body is not JSON (%q): %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want is not JSON: %v", err)
	}
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	if string(gb) != string(wb) {
		t.Errorf("request body = %s, want %s", gb, wb)
	}
}

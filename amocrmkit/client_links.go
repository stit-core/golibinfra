package amocrmkit

import (
	"context"
	"fmt"
	"net/http"
)

// EntityLink — связь (GET и POST link). При чтении главный контакт — metadata.main_contact,
// при записи — metadata.is_main (разные имена одного поля). entity_id/entity_type — только
// в массовом GET (/{entity}/links).
type EntityLink struct {
	EntityID     int64         `json:"entity_id,omitempty"`
	EntityType   string        `json:"entity_type,omitempty"`
	ToEntityID   int64         `json:"to_entity_id"`
	ToEntityType string        `json:"to_entity_type"`
	Metadata     *LinkMetadata `json:"metadata,omitempty"`
}

// LinkMetadata — метаданные связи. IsMain (запись) / MainContact (чтение) — главный контакт
// сделки; Quantity/CatalogID/PriceID — элементы каталога; UpdatedBy — от чьего имени (0 = робот).
type LinkMetadata struct {
	IsMain      *bool    `json:"is_main,omitempty"`
	MainContact *bool    `json:"main_contact,omitempty"`
	Quantity    *float64 `json:"quantity,omitempty"`
	CatalogID   int64    `json:"catalog_id,omitempty"`
	PriceID     *int64   `json:"price_id,omitempty"`
	UpdatedBy   *int64   `json:"updated_by,omitempty"`
}

// LinkInput — тело POST /{entity}/{id}/link|unlink (одиночная форма с {id} в пути — entity_id не нужен).
type LinkInput struct {
	ToEntityID   int64         `json:"to_entity_id"`
	ToEntityType string        `json:"to_entity_type"`
	Metadata     *LinkMetadata `json:"metadata,omitempty"`
}

// linkMatrix — допустимые to_entity_type по основной сущности (дока amoCRM
// «Связи сущностей»); матрица асимметрична: contacts→leads amoCRM не принимает.
var linkMatrix = map[string]map[string]bool{
	"leads":     {"contacts": true, "companies": true, "catalog_elements": true},
	"contacts":  {"companies": true, "customers": true, "catalog_elements": true},
	"companies": {"contacts": true, "leads": true, "customers": true, "catalog_elements": true},
	"customers": {"contacts": true, "companies": true, "catalog_elements": true},
}

func validateLinks(entity string, in []LinkInput) error {
	allowed, ok := linkMatrix[entity]
	if !ok {
		return fmt.Errorf("amocrmkit: link: %w: unknown entity %q", ErrInvalidRequest, entity)
	}
	for _, l := range in {
		if !allowed[l.ToEntityType] {
			return fmt.Errorf("amocrmkit: link: %w: %s→%s not allowed", ErrInvalidRequest, entity, l.ToEntityType)
		}
	}
	return nil
}

// LinkEntities — POST /api/v4/{entity}/{id}/link. Привязывает to_entity к сущности id; ответ —
// созданные связи (_embedded.links).
func (c *Client) LinkEntities(ctx context.Context, acc Account, entity string, id int64, in []LinkInput) ([]EntityLink, error) {
	if err := validateLinks(entity, in); err != nil {
		return nil, err
	}
	return sendBatch[LinkInput, EntityLink](ctx, c, acc, http.MethodPost, "entity.link", fmt.Sprintf("/api/v4/%s/%d/link", entity, id), "links", in)
}

// UnlinkEntities — POST /api/v4/{entity}/{id}/unlink (204). Для catalog_elements metadata.catalog_id обязателен.
func (c *Client) UnlinkEntities(ctx context.Context, acc Account, entity string, id int64, in []LinkInput) error {
	if err := validateLinks(entity, in); err != nil {
		return err
	}
	return c.sendNoContent(ctx, acc, http.MethodPost, "entity.unlink", fmt.Sprintf("/api/v4/%s/%d/unlink", entity, id), in)
}

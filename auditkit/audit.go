// Package auditkit — append-only audit-log: core-абстракция (Entry,
// Logger) отделена от pgstore-реализации. Кит фиксирует кто/когда/что
// сделал над каким ресурсом (actor_id, action, resource, metadata),
// чтобы compliance и forensics-запросы не приходилось склеивать из
// разрозненных логов.
package auditkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"git.stit.tech/stit-core/golibinfra/internal/clientip"
	"git.stit.tech/stit-core/golibinfra/internal/requestid"
)

// ErrInvalidEntry — запись не проходит Entry.Validate: пустое обязательное поле, значение
// длиннее предела кита, строка (и в Metadata) с NUL или не в UTF-8, Metadata без JSON-представления
// или сверх MaxMetadataBytes.
var ErrInvalidEntry = errors.New("auditkit: invalid entry")

// ErrConfig — невалидный конфиг хранилища аудита (pgstore.RetentionConfig).
var ErrConfig = errors.New("auditkit: invalid config")

// Предельные длины полей-ключей индексов audit_log — собственный предел кита, а не Postgres:
// идентификатор или имя действия длиннее — ошибка вызывающего кода, а не данные события.
const (
	// MaxActorIDLen — предел длины Entry.ActorID.
	MaxActorIDLen = 255
	// MaxActionLen — предел длины Entry.Action.
	MaxActionLen = 255
	// MaxResourceLen — предел длины Entry.Resource.
	MaxResourceLen = 255
	// MaxResourceIDLen — предел длины Entry.ResourceID.
	MaxResourceIDLen = 255
)

// MaxMetadataBytes — предел Entry.Metadata в байтах её JSON: контекст события, а не дамп
// payload'а.
const MaxMetadataBytes = 64 << 10

// Entry — одна запись аудита: кто (ActorID), что сделал (Action) над каким
// ресурсом (Resource, ResourceID) и когда (At).
type Entry struct {
	// ActorID — идентификатор инициатора (пользователь, сервис); пусто → NULL.
	ActorID string
	// Action — что сделано, например `user.create`; обязательно.
	Action string
	// Resource — тип ресурса, например `user`; обязательно.
	Resource string
	// ResourceID — идентификатор конкретного ресурса; пусто → NULL.
	ResourceID string
	// Metadata — структурированный контекст события; пусто → NULL.
	Metadata map[string]any
	// At — момент события; нулевое значение → часы базы (NOW()).
	At time.Time
	// IP — адрес инициатора; пусто → IP клиента из ctx (Enrich), без него → NULL.
	IP string
	// RequestID — корреляция с HTTP/RPC-запросом; пусто → request-id из ctx
	// (Enrich), без него → NULL.
	RequestID string
}

// Enrich — копия записи, в которой пустые RequestID и IP взяты из ctx запроса: их
// кладут middleware RequestID и ClientIP httpkit (request-id — и интерсептор
// grpckit). Явные значения не перетираются. Logger зовёт Enrich до Validate.
func (e Entry) Enrich(ctx context.Context) Entry {
	if e.RequestID == "" {
		e.RequestID = requestid.From(ctx)
	}
	if e.IP == "" {
		if addr := clientip.From(ctx); addr.IsValid() {
			e.IP = addr.String()
		}
	}
	return e
}

// Validate проверяет запись до похода в хранилище: обязательные поля, предельные длины, строки
// в UTF-8 без NUL, Metadata — как у MetadataJSON; нарушение — ErrInvalidEntry. Logger зовёт её
// (или MetadataJSON) после Enrich.
func (e Entry) Validate() error {
	_, err := e.MetadataJSON()
	return err
}

// MetadataJSON проверяет запись, как Validate, и отдаёт Metadata в JSON — то, что сохраняет
// хранилище; пустая Metadata — nil. Metadata без JSON-представления, длиннее MaxMetadataBytes,
// с NUL, не в UTF-8 (и в ключах) или с одиноким суррогатом `\ud800` в RawMessage — ErrInvalidEntry:
// Postgres JSONB такое не хранит, а замена битых байтов на U+FFFD исказила бы запись молча.
func (e Entry) MetadataJSON() ([]byte, error) {
	if err := e.checkFields(); err != nil {
		return nil, err
	}
	if len(e.Metadata) == 0 {
		return nil, nil
	}
	js, err := json.Marshal(e.Metadata)
	switch {
	case err != nil:
		return nil, fmt.Errorf("%w: Metadata has no JSON form: %w", ErrInvalidEntry, err)
	case len(js) > MaxMetadataBytes:
		return nil, fmt.Errorf("%w: Metadata is longer than %d bytes of JSON", ErrInvalidEntry, MaxMetadataBytes)
	case !utf8.Valid(js):
		return nil, fmt.Errorf("%w: Metadata is not valid UTF-8", ErrInvalidEntry)
	}
	if problem := unstorableEscape(js); problem != "" {
		return nil, fmt.Errorf("%w: Metadata %s", ErrInvalidEntry, problem)
	}
	return js, nil
}

func (e Entry) checkFields() error {
	switch {
	case e.Action == "":
		return fmt.Errorf("%w: Action is required", ErrInvalidEntry)
	case e.Resource == "":
		return fmt.Errorf("%w: Resource is required", ErrInvalidEntry)
	case len(e.ActorID) > MaxActorIDLen:
		return fmt.Errorf("%w: ActorID is longer than %d bytes", ErrInvalidEntry, MaxActorIDLen)
	case len(e.Action) > MaxActionLen:
		return fmt.Errorf("%w: Action is longer than %d bytes", ErrInvalidEntry, MaxActionLen)
	case len(e.Resource) > MaxResourceLen:
		return fmt.Errorf("%w: Resource is longer than %d bytes", ErrInvalidEntry, MaxResourceLen)
	case len(e.ResourceID) > MaxResourceIDLen:
		return fmt.Errorf("%w: ResourceID is longer than %d bytes", ErrInvalidEntry, MaxResourceIDLen)
	}
	for _, f := range []struct{ name, value string }{
		{"ActorID", e.ActorID},
		{"Action", e.Action},
		{"Resource", e.Resource},
		{"ResourceID", e.ResourceID},
		{"IP", e.IP},
		{"RequestID", e.RequestID},
	} {
		switch {
		case !utf8.ValidString(f.value):
			return fmt.Errorf("%w: %s is not valid UTF-8", ErrInvalidEntry, f.name)
		case strings.IndexByte(f.value, 0) >= 0:
			return fmt.Errorf("%w: %s contains NUL", ErrInvalidEntry, f.name)
		}
	}
	return nil
}

// unstorableEscape — escape JSON-строки, которую Postgres JSONB не хранит или которой
// encoding/json заменил битые байты: `\u0000`, одинокий суррогат и `\ufffd` (сам символ U+FFFD
// encoding/json пишет байтами, escape — только на месте не-UTF-8). Пустая строка — таких нет.
// Обратный слеш в выводе encoding/json бывает только внутри строки.
func unstorableEscape(js []byte) string {
	for i := 0; i < len(js); i++ {
		if js[i] != '\\' {
			continue
		}
		i++ // экранированный символ: `\\` — сам слеш, `\u` — код
		code, ok := escapedCode(js, i)
		if !ok {
			continue
		}
		switch {
		case code == 0:
			return "contains NUL"
		case string(js[i+1:i+5]) == "fffd":
			return "is not valid UTF-8"
		case code >= 0xDC00 && code <= 0xDFFF:
			return "has an unpaired surrogate"
		case code >= 0xD800 && code <= 0xDBFF:
			low, ok := escapedCode(js, i+6)
			if !ok || js[i+5] != '\\' || low < 0xDC00 || low > 0xDFFF {
				return "has an unpaired surrogate"
			}
			i += 6
		}
		i += 4
	}
	return ""
}

// escapedCode — код escape `\uXXXX`, чей `u` стоит на позиции i.
func escapedCode(js []byte, i int) (uint64, bool) {
	if i+4 >= len(js) || js[i] != 'u' {
		return 0, false
	}
	code, err := strconv.ParseUint(string(js[i+1:i+5]), 16, 16)
	return code, err == nil
}

// Logger — порт записи аудита; реализация в библиотеке — auditkit/pgstore.
type Logger interface {
	// Log пишет одну запись, дополненную из ctx (Entry.Enrich); невалидная Entry —
	// ErrInvalidEntry.
	Log(ctx context.Context, e Entry) error
}

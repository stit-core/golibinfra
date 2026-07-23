package yandexaikit

// Config — публичный контракт env. Имена полей/тегов стабильны (попадают в
// README § Стабильность API). Грузится через config-композицию (ENV).
//
// Две схемы auth (см. auth.go): статичный APIKey (`Authorization: Api-Key …`,
// без x-folder-id) ИЛИ IAM-token через PS256-JWT сервис-аккаунта (`Bearer …`,
// x-folder-id обязателен). APIKey приоритетнее: если он задан, IAM-поля не нужны.
type Config struct {
	// APIKey — статичный ключ сервис-аккаунта. Если задан — auth идёт по нему
	// (Api-Key схема), IAM-поля игнорируются.
	APIKey string `envconfig:"YANDEX_API_KEY"`

	// FolderID — каталог YC (x-folder-id). Обязателен для IAM-схемы; запрещён к
	// отправке при Api-Key (API уже знает каталог по ключу).
	FolderID string `envconfig:"YANDEX_FOLDER_ID"`

	// IAM (PS256-JWT) — поля авторизованного ключа сервис-аккаунта. Нужны, только
	// если APIKey пуст. PrivateKey — PEM RSA-ключа (sign JWT, PS256).
	ServiceAccountID    string `envconfig:"YANDEX_SERVICE_ACCOUNT_ID"`
	ServiceAccountKeyID string `envconfig:"YANDEX_SERVICE_ACCOUNT_KEY_ID"`
	PrivateKey          string `envconfig:"YANDEX_SERVICE_ACCOUNT_PRIVATE_KEY"`

	BaseURL      string `envconfig:"YANDEX_BASE_URL" default:"https://llm.api.cloud.yandex.net"`
	IAMURL       string `envconfig:"YANDEX_IAM_URL" default:"https://iam.api.cloud.yandex.net"`
	OperationURL string `envconfig:"YANDEX_OPERATION_URL" default:"https://operation.api.cloud.yandex.net"`
	DefaultModel string `envconfig:"YANDEX_DEFAULT_MODEL" default:"yandexgpt/latest"`

	// DataLogging — значение заголовка x-data-logging-enabled. false (дефолт)
	// просит Yandex не хранить тела запросов (PII-safe).
	DataLogging bool `envconfig:"YANDEX_DATA_LOGGING" default:"false"`
}

// usesAPIKey — true, если выбрана Api-Key схема (иначе IAM-token).
func (c Config) usesAPIKey() bool { return c.APIKey != "" }

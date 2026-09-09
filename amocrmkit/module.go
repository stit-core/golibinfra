package amocrmkit

import "go.uber.org/fx"

// Module provide'ит *Client, забирая из графа Config, httpclient.Config,
// clockkit.Clock и logkit.Logger. Обе конфигурации регистрирует caller
// (fx.Provide(amocrmkit.NewConfig, httpclient.NewConfig)); вариадик опций
// New fx не заполняет.
var Module = fx.Module(
	"amocrmkit",
	fx.Provide(New),
)

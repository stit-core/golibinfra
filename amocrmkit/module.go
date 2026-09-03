package amocrmkit

import "go.uber.org/fx"

// Module не provide'ит Config (asymmetry note в pgkit/module.go): caller
// регистрирует через fx.Provide(amocrmkit.NewConfig). New забирает из графа
// Config, httpclient.Config (свой клиент кита; fx.Provide(httpclient.NewConfig)),
// clockkit.Clock и logkit.Logger; вариадик опций fx не заполняет.
var Module = fx.Module(
	"amocrmkit",
	fx.Provide(New),
)

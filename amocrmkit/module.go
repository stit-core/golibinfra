package amocrmkit

import "go.uber.org/fx"

// Module не provide'ит Config (asymmetry note в pgkit/module.go): caller
// регистрирует через fx.Provide(amocrmkit.NewConfig). New забирает из графа
// *httpclient.Client, clockkit.Clock и logkit.Logger.
var Module = fx.Module(
	"amocrmkit",
	fx.Provide(New),
)

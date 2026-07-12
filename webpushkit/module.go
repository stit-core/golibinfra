package webpushkit

import (
	"net/http"

	"go.uber.org/fx"
)

// Module provide'ит *Sender. Config не provide'ится (asymmetry note как в
// cachekit/oidckit) — caller добавляет `fx.Provide(webpushkit.NewConfig)`.
// *http.Client опционален: без него — http.DefaultClient; передашь свой
// (httpclient/otelhttp) — Sender возьмёт его.
var Module = fx.Module(
	"webpushkit",
	fx.Provide(newSender),
)

type senderParams struct {
	fx.In

	Config Config
	Client *http.Client `optional:"true"`
}

func newSender(p senderParams) (*Sender, error) {
	return NewSender(p.Config, p.Client)
}

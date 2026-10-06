package anthropickit

import (
	"context"
	"time"

	"github.com/anthropics/anthropic-sdk-go"

	"git.stit.tech/stit-core/golibinfra/healthkit"
)

// probeName — идентификатор пробы в логе /ready.
const probeName = "anthropickit"

// probeTimeout: зависший сторонний API не должен держать /ready.
const probeTimeout = 3 * time.Second

// Probe проверяет ключ, endpoint и модель по умолчанию через Models.Get —
// вызов ничего не тарифицирует. Проба opt-in: Module её не регистрирует, потому
// что привязка readiness к стороннему платному API — решение деплоя; включают её
// через healthkit.ProvideProbe(anthropickit.Probe).
func Probe(api API) healthkit.Probe {
	check := func(ctx context.Context) error {
		raw := api.Raw()
		_, err := raw.Models.Get(ctx, api.DefaultModel(), anthropic.ModelGetParams{})
		return classify(err)
	}
	return healthkit.WithTimeout(healthkit.New(probeName, check), probeTimeout)
}

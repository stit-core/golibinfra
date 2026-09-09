package amocrmkit

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/sync/singleflight"
)

// defaultRefreshSkew — запас до истечения access-токена, при котором пара
// обновляется заранее.
const defaultRefreshSkew = 5 * time.Minute

// TokenStore — персистентность пары токенов аккаунта на стороне потребителя.
// Save обязан завершиться до того, как пара уйдёт в работу: refresh-токен
// amoCRM одноразовый, потерянная пара стоит владельцу аккаунта ре-авторизации.
type TokenStore interface {
	Load(ctx context.Context, acc Account) (TokenPair, error)
	Save(ctx context.Context, acc Account, p TokenPair) error
}

// RefreshCoordinator выдаёт актуальный access-токен аккаунта и сериализует
// refresh: конкурентные вызовы одного аккаунта делят один сетевой обмен, потому
// что второй обмен тем же refresh-токеном вернёт 401 и потеряет интеграцию.
type RefreshCoordinator struct {
	client *Client
	store  TokenStore
	app    OAuthConfig
	skew   time.Duration
	group  singleflight.Group
}

// RefreshOption — параметр сборки RefreshCoordinator.
type RefreshOption func(*RefreshCoordinator)

// WithRefreshSkew задаёт запас до истечения access-токена, при котором Token
// обновляет пару заранее (по умолчанию 5 минут).
func WithRefreshSkew(d time.Duration) RefreshOption {
	return func(r *RefreshCoordinator) { r.skew = d }
}

// NewRefreshCoordinator собирает координатор поверх клиента и token-store потребителя.
func NewRefreshCoordinator(c *Client, store TokenStore, app OAuthConfig, opts ...RefreshOption) *RefreshCoordinator {
	r := &RefreshCoordinator{client: c, store: store, app: app, skew: defaultRefreshSkew}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Token возвращает валидный access-токен аккаунта, обновляя пару, если до
// истечения осталось меньше запаса.
func (r *RefreshCoordinator) Token(ctx context.Context, acc Account) (string, error) {
	p, err := r.store.Load(ctx, acc)
	if err != nil {
		return "", fmt.Errorf("amocrmkit: token: load: %w", err)
	}
	if p.AccessToken != "" && r.client.clock.Now().Before(p.ExpiresAt().Add(-r.skew)) {
		return p.AccessToken, nil
	}
	fresh, err := r.Refresh(ctx, acc)
	if err != nil {
		return "", err
	}
	return fresh.AccessToken, nil
}

// Refresh обменивает сохранённый refresh-токен на новую пару и сохраняет её до
// возврата вызывающему. Параллельные Refresh одного аккаунта разделяют результат
// одного обмена; отмена ctx одного вызывающего не отменяет общий обмен.
func (r *RefreshCoordinator) Refresh(ctx context.Context, acc Account) (TokenPair, error) {
	shared := context.WithoutCancel(ctx)
	ch := r.group.DoChan(acc.ref().key(), func() (any, error) {
		p, err := r.store.Load(shared, acc)
		if err != nil {
			return TokenPair{}, fmt.Errorf("amocrmkit: refresh: load: %w", err)
		}
		if p.RefreshToken == "" {
			return TokenPair{}, fmt.Errorf("amocrmkit: refresh: %w: no refresh token stored", ErrAuth)
		}
		fresh, err := r.client.RefreshToken(shared, acc, r.app, p.RefreshToken)
		if err != nil {
			return TokenPair{}, err
		}
		if err := r.store.Save(shared, acc, fresh); err != nil {
			return TokenPair{}, fmt.Errorf("amocrmkit: refresh: save: %w", err)
		}
		return fresh, nil
	})

	select {
	case <-ctx.Done():
		return TokenPair{}, ctx.Err()
	case res := <-ch:
		if res.Err != nil {
			return TokenPair{}, res.Err
		}
		p, _ := res.Val.(TokenPair)
		return p, nil
	}
}

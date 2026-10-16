// Package session — сессии refresh-токенов: вход открывает сессию с абсолютным
// сроком, обновление ротирует непрозрачный токен, повтор использованного токена вне
// окна повтора отзывает сессию целиком. Хранилище и метрики — authnkit/session/pgstore,
// фейк и контрактный набор — testkit/sessiontest.
package session

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"
)

var (
	// ErrInvalidSubject — subject пуст, длиннее MaxSubjectLen, не UTF-8 или с NUL.
	ErrInvalidSubject = errors.New("authnkit/session: invalid subject")
	// ErrInvalid — токен неизвестен или его сессия отозвана: войти заново.
	ErrInvalid = errors.New("authnkit/session: refresh token invalid")
	// ErrExpired — истёк срок токена или абсолютный срок сессии: войти заново.
	ErrExpired = errors.New("authnkit/session: refresh token expired")
	// ErrReused — предъявлен уже использованный токен и это не повтор в окне Config.RetryWindow: сессия
	// отозвана целиком (Rotate возвращает *ReusedError).
	ErrReused = errors.New("authnkit/session: refresh token reused")
	// ErrInTx — Rotate вызван внутри транзакции из ctx: отзыв сессии при повторе
	// токена откатился бы вместе с ней.
	ErrInTx = errors.New("authnkit/session: Rotate must not run inside a transaction")
)

// MaxSubjectLen — предел длины subject в байтах: id пользователя короче, длиннее — ошибка
// вызывающего кода (индексная запись Postgres вместила бы и больше).
const MaxSubjectLen = 255

// MaxRetryWindow — потолок Config.RetryWindow: окно держит повтор родителя живого токена всё своё время, и вор с
// последним использованным токеном забирает сессию до следующего обновления клиента — окну хватает секунд.
const MaxRetryWindow = 5 * time.Minute

// Token — выданный refresh-токен. Value отдаётся клиенту, хранилище держит только
// его хеш (authnkit.HashToken).
type Token struct {
	// Value — непрозрачный токен authnkit.NewToken.
	Value string
	// SessionID — сессия токена: у всех токенов одной цепочки ротаций он один.
	SessionID string
	// Subject — владелец сессии (id пользователя).
	Subject string
	// ExpiresAt — срок этого токена: Config.IdleTTL от выдачи, не позже SessionExpiresAt.
	ExpiresAt time.Time
	// SessionExpiresAt — абсолютный срок сессии: Config.AbsoluteTTL от входа,
	// ротация его не продлевает.
	SessionExpiresAt time.Time
}

// ReusedError — Rotate получил уже использованный токен вне окна повтора и отозвал его сессию;
// errors.Is(err, ErrReused). SessionID и Subject — для записи аудита.
type ReusedError struct {
	SessionID string
	Subject   string
}

// Error — текст ErrReused.
func (e *ReusedError) Error() string { return ErrReused.Error() }

// Is делает ReusedError сравнимым с ErrReused.
func (e *ReusedError) Is(target error) bool { return target == ErrReused }

// Store — сессии refresh-токенов. Issue, Revoke и RevokeAll участвуют в транзакции
// из ctx (pgkit.Transactor.WithTx): отзыв идёт в одной транзакции со сменой статуса
// пользователя и откатывается вместе с ней. Rotate — только вне транзакции.
type Store interface {
	// Issue открывает сессию subject (вход) и выдаёт её первый токен;
	// невалидный subject — ErrInvalidSubject.
	Issue(ctx context.Context, subject string) (Token, error)
	// Rotate гасит токен и выдаёт следующий в той же сессии атомарно. Повтор использованного токена меньше чем через
	// Config.RetryWindow после его ротации, пока сессия жива и его преемник не использован, — повтор клиента, чей
	// ответ потерян: преемник гаснет, выдаётся новый (живым остаётся один). Прочий повтор использованного токена —
	// *ReusedError (сессия отозвана): вне окна, после использования преемника, погашенный повтором преемник.
	// Параллельные Rotate одним токеном в окне проходят по очереди — каждый получает новый токен, живым остаётся
	// последний выданный, и клиент, оставивший себе другой, на следующем обновлении получит *ReusedError, поэтому
	// клиент обновляет сессию последовательно. Неизвестный токен или отозванная сессия — ErrInvalid, истёкший срок —
	// ErrExpired, внутри транзакции из ctx — ErrInTx.
	Rotate(ctx context.Context, refresh string) (Token, error)
	// Revoke закрывает сессию токена (выход) и отдаёт её владельца — автора записи аудита
	// выхода; неизвестный токен или сессия, закрытая раньше этого вызова, — "" и nil.
	Revoke(ctx context.Context, refresh string) (subject string, err error)
	// RevokeAll закрывает все сессии subject: блокировка, удаление, смена роли или
	// пароля.
	RevokeAll(ctx context.Context, subject string) error
}

// CheckSubject — subject пригоден для сессии: непустая валидная UTF-8 без NUL, не
// длиннее MaxSubjectLen; иначе ErrInvalidSubject. Store зовёт её в Issue.
func CheckSubject(subject string) error {
	switch {
	case subject == "":
		return fmt.Errorf("%w: empty", ErrInvalidSubject)
	case len(subject) > MaxSubjectLen:
		return fmt.Errorf("%w: longer than %d bytes", ErrInvalidSubject, MaxSubjectLen)
	case !utf8.ValidString(subject):
		return fmt.Errorf("%w: not valid UTF-8", ErrInvalidSubject)
	}
	for i := range len(subject) {
		if subject[i] == 0 {
			return fmt.Errorf("%w: contains NUL", ErrInvalidSubject)
		}
	}
	return nil
}

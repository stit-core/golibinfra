package httpclient

import (
	"math"
	"net/http"
	"strconv"
	"time"
)

// ParseRetryAfter разбирает значение заголовка Retry-After в обеих формах
// RFC 9110: задержка в секундах (дробные — вверх до целой секунды) и HTTP-дата
// относительно now. ok=false, если значение пустое, отрицательное или нечитаемое;
// дата в прошлом — (0, true). Задержка, не влезающая в time.Duration, отдаётся
// как её максимум: потолок паузы выбирает вызывающий.
func ParseRetryAfter(v string, now time.Time) (time.Duration, bool) {
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.ParseFloat(v, 64); err == nil {
		if secs < 0 || math.IsNaN(secs) || math.IsInf(secs, 0) {
			return 0, false
		}
		return secondsToDuration(math.Ceil(secs)), true
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d, true
		}
		return 0, true
	}
	return 0, false
}

// secondsToDuration насыщает максимумом вместо переполнения: сервер вправе
// прислать абсурдную задержку, а time.Duration — это 292 года в наносекундах.
func secondsToDuration(secs float64) time.Duration {
	if secs >= float64(math.MaxInt64)/float64(time.Second) {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(secs) * time.Second
}

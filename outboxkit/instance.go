package outboxkit

import (
	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/timerkit"
)

// Outbox — один независимый transactional-outbox: своя таблица (через repo),
// конфиг, worker и timer-job. Приложение строит несколько Outbox'ов (по одному
// на домен/таблицу) и регистрирует их Job() в timer-группе, Publisher() — где
// нужна доменная публикация. Single-instance путь — fx Module.
type Outbox struct {
	name   string
	bus    *Bus
	worker *Worker
	cfg    Config
}

func New(name string, repo Repository, tr Transport, tx pgkit.Transactor, cfg Config) *Outbox {
	return &Outbox{
		name:   name,
		bus:    NewBus(name, repo),
		worker: NewWorker(name, repo, tr, tx, cfg),
		cfg:    cfg,
	}
}

func (o *Outbox) Name() string         { return o.name }
func (o *Outbox) Publisher() Publisher { return o.bus }

func (o *Outbox) Job() timerkit.Job {
	return timerkit.Job{
		Name:     "outboxkit.publisher." + o.name,
		Interval: o.cfg.Interval,
		Run:      o.worker.Run,
	}
}

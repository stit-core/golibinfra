package timerkit

import "go.uber.org/fx"

var Module = fx.Module("timerkit",
	fx.Invoke(startJobs),
)

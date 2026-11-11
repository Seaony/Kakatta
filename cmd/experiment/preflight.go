package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/Seaony/Kakatta/internal/diagnostics"
	"github.com/Seaony/Kakatta/internal/preflight"
)

func runSelfCheck(o options, s experimentSettings, out io.Writer) error {
	recorder, err := diagnostics.Open(experimentLogDir(o), s.Timezone)
	if err != nil {
		return err
	}
	defer recorder.Close()
	ctx, stop := context.WithTimeout(context.Background(), time.Minute)
	defer stop()
	checks := preflight.Run(ctx, preflight.Options{DataDir: o.Data, LogDir: experimentLogDir(o), Timezone: s.Timezone, Proxy: o.Proxy, WebGL: o.WebGL})
	passed := true
	for _, check := range checks {
		if !check.Passed {
			passed = false
		}
		if err := recorder.Event("环境自检", map[string]any{"结果": check}); err != nil {
			return err
		}
		events := recorder.Events()
		if err := json.NewEncoder(out).Encode(events[len(events)-1]); err != nil {
			return err
		}
	}
	if err := recorder.Summary(map[string]any{"version": 1, "mode": "preflight", "passed": passed, "checks": checks}); err != nil {
		return err
	}
	if !passed {
		return errors.New("环境自检未通过，请按每项 action 提示处理后重试；没有访问闲鱼")
	}
	return nil
}

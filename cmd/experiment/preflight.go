package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"

	"github.com/Seaony/Kakatta/internal/diagnostics"
	"github.com/Seaony/Kakatta/internal/locales"
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
	language := locales.Select("system", os.Getenv("LANG"))
	checks := preflight.Run(ctx, preflight.Options{DataDir: o.Data, LogDir: experimentLogDir(o), Timezone: s.Timezone, Proxy: o.Proxy, WebGL: o.WebGL, Language: language})
	passed := true
	for _, check := range checks {
		if !check.Passed {
			passed = false
		}
		if err := recorder.Event(experimentCheckText(language, "event_name"), map[string]any{"结果": check}); err != nil {
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
		return errors.New(experimentCheckText(language, "experiment_failed"))
	}
	return nil
}

func experimentCheckText(language locales.Language, code string) string {
	text, _ := locales.Format(language, locales.Message{Code: "preflight." + code})
	return text
}

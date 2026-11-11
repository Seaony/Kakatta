package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/Seaony/Kakatta/internal/buildinfo"
	"github.com/Seaony/Kakatta/internal/config"
	"github.com/Seaony/Kakatta/internal/diagnostics"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/xianyu"
)

func serviceEnvironment(cfg config.Config, lookup config.Lookup) map[string]any {
	commit := buildinfo.Commit
	modified := false
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" && commit == "unknown" {
				commit = setting.Value
			}
			if setting.Key == "vcs.modified" {
				modified = setting.Value == "true"
			}
		}
	}
	chrome := "unavailable"
	if version, err := browser.ExecutableVersion(context.Background()); err == nil {
		chrome = version
	}
	fields := map[string]any{
		"version": buildinfo.Version, "commit": commit, "modified": modified,
		"architecture": runtime.GOARCH, "os": runtime.GOOS, "chrome_version": chrome,
		"webgl": "off", "sandbox": true, "timezone": cfg.Environment.TZ,
	}
	fields["rules_version"] = xianyu.WebProtocol().Version
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy"} {
		_, fields[name+"_present"] = lookup(name)
	}
	return fields
}

func safeStartupSettings(snapshot settings.Snapshot) map[string]any {
	values := map[string]any{}
	for key, raw := range snapshot.Values {
		var value any
		if json.Unmarshal(raw, &value) == nil {
			values[key] = value
		}
	}
	return values
}

func runtimeDiagnostics(cfg config.Config, generation string) func(string, map[string]any) error {
	return func(event string, fields map[string]any) error {
		zone := cfg.Environment.TZ
		if _, err := time.LoadLocation(zone); zone == "" || err != nil {
			zone = "Asia/Shanghai"
		}
		recorder, err := diagnostics.Open(filepath.Join(cfg.DataDir, "diagnostics", "storage"), zone)
		if err != nil {
			return err
		}
		fields["generation"] = generation
		return errors.Join(recorder.Event(event, fields), recorder.Summary(fields), recorder.Close())
	}
}

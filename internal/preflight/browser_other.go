//go:build !linux

package preflight

import "context"

func browserChecks(_ context.Context, o Options) []Check {
	var checks []Check
	for _, id := range []string{"display", "chrome", "remote", "cleanup"} {
		checks = append(checks, Check{ID: id, Detail: text(o.Language, "linux_required", nil), Action: text(o.Language, "container_remedy", nil)})
	}
	return checks
}

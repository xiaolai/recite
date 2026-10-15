package usage

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/xiaolai/sayso/go/codexapp"
)

// FormatPlan renders the Codex plan status for `usage` and `doctor` (OBS-04): used percent,
// window and reset time of each window, and whether ordinary usage is allowed.
func FormatPlan(rl codexapp.RateLimits) string {
	var b strings.Builder
	plan := rl.PlanType
	if plan == "" {
		plan = "unknown"
	}
	fmt.Fprintf(&b, "Codex plan: %s\n", plan)
	for _, w := range rl.Windows {
		reset := "not reported"
		if !w.ResetsAt.IsZero() {
			reset = w.ResetsAt.UTC().Format("2006-01-02 15:04 UTC")
		}
		fmt.Fprintf(&b, "  %s window (%s): %s%% used, resets %s\n", w.Name, windowString(w.WindowMins),
			strconv.FormatFloat(w.UsedPercent, 'f', -1, 64), reset)
	}
	allowed := "no"
	switch {
	case !rl.OrdinaryUsageKnown: // Codex omitted the field: not a denial
		allowed = "not reported"
	case rl.OrdinaryUsageAllowed:
		allowed = "yes"
	}
	fmt.Fprintf(&b, "  ordinary usage allowed: %s\n", allowed)
	return b.String()
}

func windowString(mins int) string {
	switch {
	case mins <= 0:
		return "unknown length"
	case mins%1440 == 0:
		return strconv.Itoa(mins/1440) + " d"
	case mins%60 == 0:
		return strconv.Itoa(mins/60) + " h"
	}
	return strconv.Itoa(mins) + " min"
}

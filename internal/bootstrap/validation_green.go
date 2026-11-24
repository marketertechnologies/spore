package bootstrap

import (
	"errors"
	"fmt"
	"strings"

	"github.com/versality/spore/internal/lints"
)

func detectValidationGreen(root string) (string, error) {
	if root == "" {
		return "", errors.New("validation-green: empty root")
	}
	all := lints.Default()
	var blockers []string
	var summary []string
	for _, l := range all {
		issues, err := l.Run(root)
		if err != nil {
			summary = append(summary, fmt.Sprintf("%s: error: %v", l.Name(), err))
			blockers = append(blockers, fmt.Sprintf("%s errored: %v", l.Name(), err))
			continue
		}
		var errs []lints.Issue
		for _, i := range issues {
			if !i.Warn {
				errs = append(errs, i)
			}
		}
		summary = append(summary, fmt.Sprintf("%s: %d", l.Name(), len(errs)))
		if len(errs) > 0 {
			head := errs[0].String()
			blockers = append(blockers, fmt.Sprintf("%s: %d issue(s); first: %s", l.Name(), len(errs), head))
		}
	}
	if len(blockers) > 0 {
		return "", fmt.Errorf("lints reported issues: %s", strings.Join(blockers, "; "))
	}
	return strings.Join(summary, ", "), nil
}

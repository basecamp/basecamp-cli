package views

import "fmt"

// boostLabel returns "1 boost" or "N boosts".
func boostLabel(n int) string {
	if n == 1 {
		return "1 boost"
	}
	return fmt.Sprintf("%d boosts", n)
}

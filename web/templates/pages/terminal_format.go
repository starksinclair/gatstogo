package pages

import "fmt"

func fmtInt(n int64) string {
	return fmt.Sprintf("%d", n)
}

func fmtSeconds(n int) string {
	if n < 0 {
		n = 0
	}
	return fmt.Sprintf("%02d", n)
}

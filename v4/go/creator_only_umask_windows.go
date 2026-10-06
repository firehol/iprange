//go:build windows

package iprangedb

func setUmask(int) int { return 0 }

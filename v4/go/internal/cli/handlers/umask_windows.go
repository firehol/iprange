//go:build windows

package handlers

func setUmask(int) int { return 0 }

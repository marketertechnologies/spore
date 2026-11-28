//go:build !linux

package doctor

func hasACL(string) bool { return false }

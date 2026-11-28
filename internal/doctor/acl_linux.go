//go:build linux

package doctor

import "syscall"

// hasACL reports whether path carries a POSIX access ACL beyond its
// mode bits: the xattr only exists for an extended ACL.
func hasACL(path string) bool {
	n, err := syscall.Getxattr(path, "system.posix_acl_access", nil)
	return err == nil && n > 0
}

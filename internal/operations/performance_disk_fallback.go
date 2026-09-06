//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package operations

// performanceDiskSpaceInfo returns an empty but schema-compatible result on
// platforms without the Unix statfs API used by the supported release builds.
func performanceDiskSpaceInfo(string) PerformanceDiskSpaceInfo {
	return PerformanceDiskSpaceInfo{}
}

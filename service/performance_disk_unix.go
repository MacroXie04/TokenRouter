//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package service

import (
	"math"

	"golang.org/x/sys/unix"
)

func performanceDiskSpaceInfo(path string) PerformanceDiskSpaceInfo {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil || stat.Bsize <= 0 {
		return PerformanceDiskSpaceInfo{}
	}
	blockSize := uint64(stat.Bsize)
	if uint64(stat.Blocks) > math.MaxUint64/blockSize || uint64(stat.Bavail) > math.MaxUint64/blockSize ||
		uint64(stat.Bfree) > math.MaxUint64/blockSize {
		return PerformanceDiskSpaceInfo{}
	}
	total := uint64(stat.Blocks) * blockSize
	free := uint64(stat.Bavail) * blockSize
	usedBlocks := uint64(stat.Blocks)
	if uint64(stat.Bfree) <= usedBlocks {
		usedBlocks -= uint64(stat.Bfree)
	} else {
		usedBlocks = 0
	}
	used := usedBlocks * blockSize
	info := PerformanceDiskSpaceInfo{Total: total, Free: free, Used: used}
	if total > 0 {
		info.UsedPercent = float64(used) / float64(total) * 100
	}
	return info
}

//go:build !unix

package hwinfo

func diskUsage(string) (total, avail uint64, ok bool) { return 0, 0, false }

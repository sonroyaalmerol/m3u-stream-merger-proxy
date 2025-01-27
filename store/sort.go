package store

import (
	"sort"
	"strings"
	"os"
)

func SortStreams(s []StreamInfo) {
	key := os.Getenv("SORTING_KEY")
	dir := os.Getenv("SORTING_DIRECTION")

	switch key {
	case "tvg-id":
		sort.SliceStable(s, func(i, j int) bool {
			if strings.ToLower(dir) == `desc` {
				return s[i].TvgID > s[j].TvgID
			}
			return s[i].TvgID < s[j].TvgID
		})
	case "tvg-chno":
		sort.SliceStable(s, func(i, j int) bool {
			if strings.ToLower(dir) == `desc` {
				return s[i].TvgChNo > s[j].TvgChNo
			}
			return s[i].TvgChNo < s[j].TvgChNo
		})
	case "tvg-group":
		sort.SliceStable(s, func(i, j int) bool {
			if strings.ToLower(dir) == `desc` {
				return s[i].Group > s[j].Group
			}
			return s[i].Group < s[j].Group
		})
	case "tvg-type":
		sort.SliceStable(s, func(i, j int) bool {
			if strings.ToLower(dir) == `desc` {
				return s[i].TvgType > s[j].TvgType
			}
			return s[i].TvgType < s[j].TvgType
		})
	default:
		sort.SliceStable(s, func(i, j int) bool {
			if strings.ToLower(dir) == `desc` {
				return s[i].Title > s[j].Title
			}
			return s[i].Title < s[j].Title
		})
	}
}

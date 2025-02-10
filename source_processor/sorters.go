package sourceproc

import (
	"sort"
	"strconv"
	"strings"
)

func SortStreamSubUrls(urls map[string]string) []string {
	type urlInfo struct {
		key string
		idx int
	}

	urlInfos := make([]urlInfo, 0, len(urls))
	for key, url := range urls {
		idxStr := strings.SplitN(url, ":::", 2)[0]
		idx, _ := strconv.Atoi(idxStr)
		urlInfos = append(urlInfos, urlInfo{key, idx})
	}

	sort.Slice(urlInfos, func(i, j int) bool {
		return urlInfos[i].idx < urlInfos[j].idx
	})

	result := make([]string, len(urlInfos))
	for i, info := range urlInfos {
		result[i] = info.key
	}
	return result
}

func getSortFunction(key, dir string) func(*StreamInfo, *StreamInfo, string, string) bool {
	tieBreaker := func(s1, s2 *StreamInfo) bool {
		if s1.SourceM3U != s2.SourceM3U {
			return s1.SourceM3U < s2.SourceM3U
		}
		return s1.SourceIndex < s2.SourceIndex
	}

	switch key {
	case "tvg-id":
		return func(s1, s2 *StreamInfo, _, _ string) bool {
			num1, err1 := strconv.Atoi(s1.TvgID)
			num2, err2 := strconv.Atoi(s2.TvgID)

			if err1 == nil && err2 == nil {
				if num1 != num2 {
					if dir == "desc" {
						return num1 > num2
					}
					return num1 < num2
				}
			} else if s1.TvgID != s2.TvgID {
				if dir == "desc" {
					return s1.TvgID > s2.TvgID
				}
				return s1.TvgID < s2.TvgID
			}
			return tieBreaker(s1, s2)
		}
	case "tvg-chno":
		return func(s1, s2 *StreamInfo, _, _ string) bool {
			num1, err1 := strconv.Atoi(s1.TvgChNo)
			num2, err2 := strconv.Atoi(s2.TvgChNo)

			if err1 == nil && err2 == nil {
				if num1 != num2 {
					if dir == "desc" {
						return num1 > num2
					}
					return num1 < num2
				}
			} else if s1.TvgChNo != s2.TvgChNo {
				if dir == "desc" {
					return s1.TvgChNo > s2.TvgChNo
				}
				return s1.TvgChNo < s2.TvgChNo
			}

			return tieBreaker(s1, s2)
		}
	case "tvg-group":
		return func(s1, s2 *StreamInfo, _, _ string) bool {
			if s1.Group != s2.Group {
				if dir == "desc" {
					return s1.Group > s2.Group
				}
				return s1.Group < s2.Group
			}
			return tieBreaker(s1, s2)
		}
	case "tvg-type":
		return func(s1, s2 *StreamInfo, _, _ string) bool {
			if s1.TvgType != s2.TvgType {
				if dir == "desc" {
					return s1.TvgType > s2.TvgType
				}
				return s1.TvgType < s2.TvgType
			}
			return tieBreaker(s1, s2)
		}
	case "source":
		return func(s1, s2 *StreamInfo, _, _ string) bool {
			num1, err1 := strconv.Atoi(s1.SourceM3U)
			num2, err2 := strconv.Atoi(s2.SourceM3U)

			if err1 == nil && err2 == nil {
				if num1 != num2 {
					return num1 < num2
				}
			} else if s1.SourceM3U != s2.SourceM3U {
				return s1.SourceM3U < s2.SourceM3U
			}

			return s1.SourceIndex < s2.SourceIndex
		}
	default:
		return func(s1, s2 *StreamInfo, _, _ string) bool {
			if dir == "desc" {
				return s1.Title > s2.Title
			}
			return s1.Title < s2.Title
		}
	}
}

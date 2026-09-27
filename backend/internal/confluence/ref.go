package confluence

import (
	"net/url"
	"strings"
)

// PageRef is what a user may paste to designate a page: a numeric id, or a
// URL in one of the formats produced by Confluence.
type PageRef struct {
	ID       string
	SpaceKey string
	Title    string
}

// ParsePageRef extracts a page reference from user input. Supported forms:
//
//	123456
//	https://host/pages/viewpage.action?pageId=123456
//	https://host/spaces/KEY/pages/123456/Title
//	https://host/display/KEY/Page+Title
//
// It returns ok=false when the input is plain text (to be used as a search).
func ParsePageRef(input string) (PageRef, bool) {
	input = strings.TrimSpace(input)
	if isNumeric(input) {
		return PageRef{ID: input}, true
	}
	u, err := url.Parse(input)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return PageRef{}, false
	}
	if id := u.Query().Get("pageId"); isNumeric(id) {
		return PageRef{ID: id}, true
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i, s := range segs {
		if s == "pages" && i+1 < len(segs) && isNumeric(segs[i+1]) {
			return PageRef{ID: segs[i+1]}, true
		}
		if s == "display" && i+2 < len(segs) {
			title, err := url.PathUnescape(strings.ReplaceAll(segs[i+2], "+", " "))
			if err != nil {
				return PageRef{}, false
			}
			return PageRef{SpaceKey: segs[i+1], Title: title}, true
		}
	}
	return PageRef{}, false
}

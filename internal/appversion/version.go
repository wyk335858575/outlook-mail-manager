package appversion

import (
	"errors"
	"strconv"
	"strings"
)

var ErrInvalid = errors.New("invalid application version")

type Version struct {
	Major int
	Minor int
	Patch int
}

func Parse(value string) (Version, error) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return Version{}, ErrInvalid
	}
	values := make([]int, len(parts))
	for index, part := range parts {
		if part == "" || len(part) > 1 && part[0] == '0' {
			return Version{}, ErrInvalid
		}
		parsed, err := strconv.Atoi(part)
		if err != nil || parsed < 0 {
			return Version{}, ErrInvalid
		}
		values[index] = parsed
	}
	return Version{Major: values[0], Minor: values[1], Patch: values[2]}, nil
}

func Greater(latest, current string) bool {
	l, err := Parse(latest)
	if err != nil {
		return false
	}
	c, err := Parse(current)
	if err != nil {
		return false
	}
	return l.Major > c.Major || l.Major == c.Major && (l.Minor > c.Minor || l.Minor == c.Minor && l.Patch > c.Patch)
}

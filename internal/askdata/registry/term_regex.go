package registry

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	TermRegexpTimeout       = 10 * time.Millisecond
	MaxTermRegexpBytes      = 1024
	MaxTermRegexpInputBytes = 64 << 10
	MaxTermRegexpRepeat     = 32
)

var (
	ErrTermRegexUnsafe  = errors.New("TERM_REGEX_UNSAFE: regex contains an unsafe construct")
	ErrTermRegexTimeout = errors.New("TERM_REGEX_TIMEOUT: regex matching exceeded the governed deadline")
)

// SafeTermRegexp is a compiled RE2 expression restricted to literals,
// character classes, anchors and bounded repetition. The restricted grammar
// stays reviewable and portable even though Go's regexp engine is already
// resistant to exponential backtracking.
type SafeTermRegexp struct{ expression *regexp.Regexp }

func CompileSafeTermRegexp(pattern string) (*SafeTermRegexp, error) {
	if !validSafeTermRegexpGrammar(pattern) {
		return nil, ErrTermRegexUnsafe
	}
	expression, err := regexp.Compile(pattern)
	if err != nil {
		return nil, ErrTermRegexUnsafe
	}
	return &SafeTermRegexp{expression: expression}, nil
}

func (expression *SafeTermRegexp) Match(ctx context.Context, value string) (bool, error) {
	if expression == nil || expression.expression == nil || ctx == nil ||
		!utf8.ValidString(value) || len(value) > MaxTermRegexpInputBytes {
		return false, ErrTermRegexUnsafe
	}
	if ctx.Err() != nil {
		return false, ErrTermRegexTimeout
	}
	matchContext, cancel := context.WithTimeout(ctx, TermRegexpTimeout)
	defer cancel()
	result := make(chan bool, 1)
	go func() { result <- expression.expression.MatchString(value) }()
	select {
	case matched := <-result:
		return matched, nil
	case <-matchContext.Done():
		return false, ErrTermRegexTimeout
	}
}

func safeTermRegexp(pattern string) bool {
	_, err := CompileSafeTermRegexp(pattern)
	return err == nil
}

func validSafeTermRegexpGrammar(pattern string) bool {
	if pattern == "" || len(pattern) > MaxTermRegexpBytes ||
		strings.TrimSpace(pattern) != pattern || !utf8.ValidString(pattern) {
		return false
	}
	inClass := false
	classHasValue := false
	escaped := false
	canRepeat := false
	for index := 0; index < len(pattern); {
		character, size := utf8.DecodeRuneInString(pattern[index:])
		if character == utf8.RuneError && size == 1 {
			return false
		}
		if escaped {
			if character >= '0' && character <= '9' {
				return false
			}
			escaped = false
			if inClass {
				classHasValue = true
			} else {
				canRepeat = true
			}
			index += size
			continue
		}
		if character == '\\' {
			escaped = true
			index += size
			continue
		}
		if inClass {
			switch character {
			case ']':
				if !classHasValue {
					return false
				}
				inClass, canRepeat = false, true
			case '[':
				return false
			default:
				classHasValue = true
			}
			index += size
			continue
		}
		switch character {
		case '[':
			inClass, classHasValue, canRepeat = true, false, false
			index += size
		case ']':
			return false
		case '^':
			if index != 0 {
				return false
			}
			canRepeat = false
			index += size
		case '$':
			if index+size != len(pattern) {
				return false
			}
			canRepeat = false
			index += size
		case '{':
			if !canRepeat {
				return false
			}
			end := strings.IndexByte(pattern[index:], '}')
			if end < 0 {
				return false
			}
			end += index
			if !validTermRepeat(pattern[index+1 : end]) {
				return false
			}
			canRepeat = false
			index = end + 1
		case '}', '.', '*', '+', '?', '(', ')', '|':
			return false
		default:
			if character < 0x20 || character == 0x7f {
				return false
			}
			canRepeat = true
			index += size
		}
	}
	return !escaped && !inClass
}

func validTermRepeat(raw string) bool {
	parts := strings.Split(raw, ",")
	if len(parts) < 1 || len(parts) > 2 || parts[0] == "" || len(parts) == 2 && parts[1] == "" {
		return false
	}
	lower, err := strconv.Atoi(parts[0])
	if err != nil || lower < 0 || lower > MaxTermRegexpRepeat {
		return false
	}
	upper := lower
	if len(parts) == 2 {
		upper, err = strconv.Atoi(parts[1])
		if err != nil || upper < lower || upper > MaxTermRegexpRepeat {
			return false
		}
	}
	return true
}
